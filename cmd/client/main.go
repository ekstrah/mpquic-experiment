// Command client generates a random payload of a given size, splits it into
// chunks, and sends it to the server striped across one independent QUIC
// connection per configured local path (source IP), each with its own
// pluggable congestion control.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	quic "github.com/quic-go/quic-go"

	"mpquic-experiment/internal/ccmodules"
	"mpquic-experiment/internal/metrics"
	"mpquic-experiment/internal/scheduler"
	"mpquic-experiment/internal/tlsconfig"
	"mpquic-experiment/internal/transfer"
)

func main() {
	serverAddr := flag.String("server", "", "server address, host:port (required)")
	localAddrs := flag.String("local", "", "comma-separated local source IPs, one per path (default: one path, OS-chosen address)")
	size := flag.Uint64("size", 10*1024*1024, "total payload size in bytes (ignored when -continuous is set)")
	chunkSize := flag.Uint("chunk-size", 32*1024, "chunk size in bytes")
	schedName := flag.String("scheduler", "roundrobin", fmt.Sprintf("scheduler to use (%s)", strings.Join(scheduler.Names(), ", ")))
	ccList := flag.String("cc", "cubic", fmt.Sprintf("comma-separated congestion control per path, or one value for all paths (%s)", strings.Join(ccmodules.Names(), ", ")))
	outPrefix := flag.String("out", "client-results", "output file prefix for -results.json / -results.csv")
	progressEvery := flag.Duration("progress-interval", time.Second, "console progress print interval")
	continuous := flag.Bool("continuous", false, "stream continuously as variable-size bursts instead of one fixed -size transfer")
	duration := flag.Duration("duration", 0, "-continuous mode: how long to keep streaming (0 = until Ctrl+C)")
	burstMinSize := flag.Uint64("burst-min-size", 4*1024, "-continuous mode: minimum burst size in bytes")
	burstMaxSize := flag.Uint64("burst-max-size", 1024*1024, "-continuous mode: maximum burst size in bytes")
	burstInterval := flag.Duration("burst-interval", time.Second, "-continuous mode: time between bursts")
	flag.Parse()

	if *serverAddr == "" {
		log.Fatal("client: -server is required")
	}
	if *continuous && (*burstMinSize == 0 || *burstMaxSize < *burstMinSize) {
		log.Fatal("client: -continuous requires -burst-max-size >= -burst-min-size > 0")
	}

	locals := parseLocals(*localAddrs)
	numPaths := len(locals)

	ccNames, err := expandCC(*ccList, numPaths)
	if err != nil {
		log.Fatalf("client: %v", err)
	}
	schedFactory, err := scheduler.Get(*schedName)
	if err != nil {
		log.Fatalf("client: %v", err)
	}

	totalSize := *size
	if *continuous {
		totalSize = 0 // 0 tells the server this is a continuous session (see ControlHeader doc)
	}

	sessID := transfer.NewSessionID()
	ctrl := transfer.ControlHeader{
		SessionID: sessID,
		TotalSize: totalSize,
		ChunkSize: uint32(*chunkSize),
		NumPaths:  uint32(numPaths),
		Scheduler: *schedName,
	}

	paths := make([]*pathConn, numPaths)
	for i, local := range locals {
		pc, err := dialPath(context.Background(), i, local, *serverAddr, ccNames[i])
		if err != nil {
			log.Fatalf("client: dialing path %d (local %s): %v", i, local, err)
		}
		paths[i] = pc
	}
	defer func() {
		for _, pc := range paths {
			pc.conn.CloseWithError(0, "")
		}
	}()

	for i, pc := range paths {
		var err error
		if i == 0 {
			err = transfer.WriteControlHeader(pc.stream, ctrl)
		} else {
			err = transfer.WritePathHello(pc.stream, transfer.PathHello{SessionID: sessID, PathIndex: uint32(i)})
		}
		if err != nil {
			log.Fatalf("client: path %d: sending preamble: %v", i, err)
		}
	}

	sched := schedFactory()
	start := time.Now()

	sessIDStr := fmt.Sprintf("%x", sessID)
	rttLog := &metrics.SampleLog[metrics.RTTSample]{}
	rttCtx, stopRTT := context.WithCancel(context.Background())
	var rttWG sync.WaitGroup
	for _, pc := range paths {
		rttWG.Add(1)
		go func(pc *pathConn) {
			defer rttWG.Done()
			sampleRTT(rttCtx, pc, start, sessIDStr, rttLog)
		}(pc)
	}

	if *continuous {
		runContinuous(continuousConfig{
			paths: paths, sched: sched, start: start, sessIDStr: sessIDStr,
			minSize: *burstMinSize, maxSize: *burstMaxSize, interval: *burstInterval,
			duration: *duration, chunkSize: uint32(*chunkSize), outPrefix: *outPrefix,
		})
	} else {
		log.Printf("client: generating %d byte random payload", *size)
		payload, err := transfer.GenerateRandomPayload(*size)
		if err != nil {
			log.Fatalf("client: %v", err)
		}
		chunks := transfer.Split(payload, uint32(*chunkSize), 0, *size)
		progress := metrics.NewProgressPrinter(*size, *progressEvery)
		sendChunks(chunks, paths, sched, progress)
		progress.Close()
	}

	stopRTT()
	rttWG.Wait()

	drainPaths(paths)

	if !*continuous {
		result := buildResult(sessID, ctrl, paths, start)
		result.Print()
		if err := result.WriteJSON(*outPrefix + "-results.json"); err != nil {
			log.Printf("client: write json: %v", err)
		}
		if err := result.WriteCSV(*outPrefix + "-results.csv"); err != nil {
			log.Printf("client: write csv: %v", err)
		}
	}
	if err := metrics.WriteRTTSamplesCSV(*outPrefix+"-client-rtt.csv", rttLog.Snapshot()); err != nil {
		log.Printf("client: write rtt csv: %v", err)
	}
}

// continuousConfig bundles runContinuous's parameters -- enough of them
// that a plain argument list would be unwieldy and error-prone to call.
type continuousConfig struct {
	paths            []*pathConn
	sched            scheduler.Scheduler
	start            time.Time
	sessIDStr        string
	minSize, maxSize uint64
	interval         time.Duration
	duration         time.Duration
	chunkSize        uint32
	outPrefix        string
}

// runContinuous sends one burst immediately, then one every cfg.interval,
// each a random size in [minSize,maxSize], until cfg.duration elapses (0 =
// never) or the process receives SIGINT/SIGTERM. Reuses sendChunks/
// scheduler/drainPaths exactly as the one-shot path does -- a burst is just
// "one more batch of chunks" to that machinery, nothing about it is
// continuous-mode-specific.
func runContinuous(cfg continuousConfig) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	ctx := context.Background()
	if cfg.duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.duration)
		defer cancel()
	}

	var burstLog metrics.SampleLog[metrics.BurstSample]
	var burstID uint64

	sendBurst := func() {
		burstID++
		size := cfg.minSize
		if cfg.maxSize > cfg.minSize {
			size += uint64(rand.Int63n(int64(cfg.maxSize - cfg.minSize + 1)))
		}
		data, err := transfer.GenerateRandomPayload(size)
		if err != nil {
			log.Printf("client: burst %d: %v", burstID, err)
			return
		}
		chunks := transfer.Split(data, cfg.chunkSize, burstID, size)

		before, beforeChunks := snapshotSent(cfg.paths)
		beforePerPath := snapshotSentPerPath(cfg.paths)
		startMs := time.Since(cfg.start).Milliseconds()
		sendChunks(chunks, cfg.paths, cfg.sched, nil)
		after, afterChunks := snapshotSent(cfg.paths)
		afterPerPath := snapshotSentPerPath(cfg.paths)
		endMs := time.Since(cfg.start).Milliseconds()

		sent := after - before
		log.Printf("client: burst %d: sent %d/%d bytes (%d chunks)", burstID, sent, size, afterChunks-beforeChunks)
		burstLog.Add(metrics.BurstSample{
			SessionID: cfg.sessIDStr, Role: "client", BurstID: burstID, PathIndex: -1,
			BytesExpected: size, BytesReceived: sent,
			Chunks: afterChunks - beforeChunks, Complete: sent == size,
			StartMs: startMs, EndMs: endMs,
		})
		for idx, a := range afterPerPath {
			b := beforePerPath[idx]
			burstLog.Add(metrics.BurstSample{
				SessionID: cfg.sessIDStr, Role: "client", BurstID: burstID, PathIndex: idx,
				BytesExpected: size, BytesReceived: a.bytes - b.bytes,
				Chunks: a.chunks - b.chunks, Complete: a.bytes-b.bytes == size,
				StartMs: startMs, EndMs: endMs,
			})
		}
		if err := metrics.WriteBurstSamplesCSV(cfg.outPrefix+"-bursts.csv", burstLog.Snapshot()); err != nil {
			log.Printf("client: write bursts csv: %v", err)
		}
	}

	sendBurst()
	ticker := time.NewTicker(cfg.interval)
	defer ticker.Stop()
	for {
		select {
		case <-sigCh:
			log.Printf("client: received interrupt, stopping burst generation")
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			sendBurst()
		}
	}
}

// snapshotSent totals bytes/chunks successfully written so far across every
// path, so runContinuous can compute one burst's delta.
func snapshotSent(paths []*pathConn) (bytes uint64, chunks int) {
	for _, pc := range paths {
		pc.mu.Lock()
		bytes += pc.bytesSent
		chunks += pc.chunksSent
		pc.mu.Unlock()
	}
	return bytes, chunks
}

// pathSnapshot is snapshotSent's per-path breakdown -- which path(s) a
// scheduler actually placed a burst's chunks on, visible per burst rather
// than only as a whole-session total.
type pathSnapshot struct {
	bytes  uint64
	chunks int
}

func snapshotSentPerPath(paths []*pathConn) map[int]pathSnapshot {
	out := make(map[int]pathSnapshot, len(paths))
	for _, pc := range paths {
		pc.mu.Lock()
		out[pc.index] = pathSnapshot{bytes: pc.bytesSent, chunks: pc.chunksSent}
		pc.mu.Unlock()
	}
	return out
}

// drainTimeout bounds how long drainPath waits for the server's delivery
// confirmation before giving up and closing anyway.
const drainTimeout = 5 * time.Second

// drainPaths closes every path's stream write-side and waits (concurrently,
// one path's stall shouldn't block another) for the server's delivery
// confirmation before the caller closes the underlying connections. This is
// the piece that was missing before: SendStream.Close() only schedules the
// FIN, it doesn't wait for it (or any trailing data) to be sent or acked,
// so closing the QUIC connection right after used to race in-flight tail
// chunks against CloseWithError abandoning them.
func drainPaths(paths []*pathConn) {
	var wg sync.WaitGroup
	for _, pc := range paths {
		wg.Add(1)
		go func(pc *pathConn) {
			defer wg.Done()
			drainPath(pc)
		}(pc)
	}
	wg.Wait()
}

// drainPath closes pc's stream and blocks until the server's 1-byte drain
// ack arrives, proving every chunk (and the FIN) was received in order.
func drainPath(pc *pathConn) {
	if err := pc.stream.Close(); err != nil {
		log.Printf("client: path %d: closing stream: %v", pc.index, err)
		return
	}
	if err := pc.stream.SetReadDeadline(time.Now().Add(drainTimeout)); err != nil {
		log.Printf("client: path %d: set read deadline: %v", pc.index, err)
	}
	var ack [1]byte
	if _, err := io.ReadFull(pc.stream, ack[:]); err != nil {
		log.Printf("client: path %d: no delivery confirmation from server (%v) -- some tail data may not have arrived", pc.index, err)
	}
}

// sampleRTT periodically records the path's RTT (as seen by the client's
// QUIC stack) until ctx is cancelled. LatestRTT is the raw per-ACK sample;
// SmoothedRTT is included alongside it for reference.
func sampleRTT(ctx context.Context, pc *pathConn, start time.Time, sessIDStr string, rttLog *metrics.SampleLog[metrics.RTTSample]) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stats := pc.conn.ConnectionStats()
			rttLog.Add(metrics.RTTSample{
				SessionID:     sessIDStr,
				PathIndex:     pc.index,
				TMs:           time.Since(start).Milliseconds(),
				LatestRTTMs:   float64(stats.LatestRTT.Microseconds()) / 1000,
				SmoothedRTTMs: float64(stats.SmoothedRTT.Microseconds()) / 1000,
			})
		}
	}
}

type pathConn struct {
	index      int
	conn       *quic.Conn
	stream     *quic.Stream
	ccName     string
	mu         sync.Mutex
	bytesSent  uint64
	chunksSent int
}

func dialPath(ctx context.Context, index int, local, server string, ccName string) (*pathConn, error) {
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: local2IP(local)})
	if err != nil {
		return nil, fmt.Errorf("binding local address: %w", err)
	}
	serverUDPAddr, err := net.ResolveUDPAddr("udp", server)
	if err != nil {
		return nil, fmt.Errorf("resolving server address: %w", err)
	}
	ccFactory, err := ccmodules.Get(ccName)
	if err != nil {
		return nil, err
	}
	conn, err := quic.Dial(ctx, udpConn, serverUDPAddr, tlsconfig.ClientConfig(), &quic.Config{
		CongestionControlFactory: ccFactory,
	})
	if err != nil {
		return nil, fmt.Errorf("QUIC dial: %w", err)
	}
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, fmt.Errorf("opening stream: %w", err)
	}
	return &pathConn{index: index, conn: conn, stream: stream, ccName: ccName}, nil
}

// sendChunks lets the scheduler assign each chunk to one or more paths.
// Each path has its own goroutine draining a queue, so paths are used
// concurrently -- the whole point of striping across multiple links. A
// redundant scheduler assigning a chunk to several paths sends those copies
// concurrently too.
func sendChunks(chunks []transfer.Chunk, paths []*pathConn, sched scheduler.Scheduler, progress *metrics.ProgressPrinter) {
	queues := make([]chan transfer.Chunk, len(paths))
	var sent uint64
	var sentMu sync.Mutex
	var wg sync.WaitGroup

	for i, pc := range paths {
		queues[i] = make(chan transfer.Chunk, 64)
		wg.Add(1)
		go func(pc *pathConn, q <-chan transfer.Chunk) {
			defer wg.Done()
			for c := range q {
				if err := transfer.WriteChunk(pc.stream, c); err != nil {
					log.Printf("client: path %d: writing chunk %d: %v", pc.index, c.Seq, err)
					continue
				}
				pc.mu.Lock()
				pc.bytesSent += uint64(len(c.Data))
				pc.chunksSent++
				pc.mu.Unlock()
				sentMu.Lock()
				sent += uint64(len(c.Data))
				if progress != nil {
					progress.Update(sent)
				}
				sentMu.Unlock()
			}
		}(pc, queues[i])
	}

	infos := make([]scheduler.PathInfo, len(paths))
	for _, c := range chunks {
		for i, pc := range paths {
			infos[i] = scheduler.PathInfo{Index: pc.index, RTT: pc.conn.ConnectionStats().SmoothedRTT}
		}
		for _, idx := range sched.Assign(c.Seq, infos) {
			queues[idx] <- c
		}
	}
	for _, q := range queues {
		close(q)
	}
	wg.Wait()
}

func buildResult(sessID transfer.SessionID, ctrl transfer.ControlHeader, paths []*pathConn, start time.Time) metrics.RunResult {
	r := metrics.RunResult{
		Role:       "client",
		SessionID:  fmt.Sprintf("%x", sessID),
		TotalBytes: ctrl.TotalSize,
		ChunkSize:  ctrl.ChunkSize,
		Scheduler:  ctrl.Scheduler,
		StartTime:  start,
	}
	for _, pc := range paths {
		r.Paths = append(r.Paths, metrics.PathStats{
			Index:      pc.index,
			LocalAddr:  pc.conn.LocalAddr().String(),
			RemoteAddr: pc.conn.RemoteAddr().String(),
			Bytes:      pc.bytesSent,
			Chunks:     pc.chunksSent,
			Duration:   time.Since(start),
			CC:         pc.ccName,
		})
	}
	r.Finalize(time.Now())
	return r
}

func parseLocals(s string) []string {
	if strings.TrimSpace(s) == "" {
		return []string{""}
	}
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func local2IP(s string) net.IP {
	if s == "" {
		return nil
	}
	return net.ParseIP(s)
}

func expandCC(spec string, numPaths int) ([]string, error) {
	parts := strings.Split(spec, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	if len(parts) == 1 {
		names := make([]string, numPaths)
		for i := range names {
			names[i] = parts[0]
		}
		return names, nil
	}
	if len(parts) != numPaths {
		return nil, fmt.Errorf("-cc has %d entries but there are %d paths", len(parts), numPaths)
	}
	return parts, nil
}
