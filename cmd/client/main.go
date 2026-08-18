// Command client generates a random payload of a given size, splits it into
// chunks, and sends it to the server striped across one independent QUIC
// connection per configured local path (source IP), each with its own
// pluggable congestion control.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
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
	size := flag.Uint64("size", 10*1024*1024, "total payload size in bytes")
	chunkSize := flag.Uint("chunk-size", 32*1024, "chunk size in bytes")
	schedName := flag.String("scheduler", "roundrobin", fmt.Sprintf("scheduler to use (%s)", strings.Join(scheduler.Names(), ", ")))
	ccList := flag.String("cc", "cubic", fmt.Sprintf("comma-separated congestion control per path, or one value for all paths (%s)", strings.Join(ccmodules.Names(), ", ")))
	outPrefix := flag.String("out", "client-results", "output file prefix for -results.json / -results.csv")
	progressEvery := flag.Duration("progress-interval", time.Second, "console progress print interval")
	flag.Parse()

	if *serverAddr == "" {
		log.Fatal("client: -server is required")
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

	log.Printf("client: generating %d byte random payload", *size)
	payload, hash, err := transfer.GenerateRandomPayload(*size)
	if err != nil {
		log.Fatalf("client: %v", err)
	}
	chunks := transfer.Split(payload, uint32(*chunkSize))

	sessID := transfer.NewSessionID()
	ctrl := transfer.ControlHeader{
		SessionID: sessID,
		TotalSize: *size,
		ChunkSize: uint32(*chunkSize),
		NumPaths:  uint32(numPaths),
		Scheduler: *schedName,
		Hash:      hash,
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

	progress := metrics.NewProgressPrinter(*size, *progressEvery)
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

	sendChunks(chunks, paths, sched, progress)
	progress.Close()
	stopRTT()
	rttWG.Wait()

	for _, pc := range paths {
		pc.stream.Close()
	}

	result := buildResult(sessID, ctrl, paths, start)
	result.Print()
	if err := result.WriteJSON(*outPrefix + "-results.json"); err != nil {
		log.Printf("client: write json: %v", err)
	}
	if err := result.WriteCSV(*outPrefix + "-results.csv"); err != nil {
		log.Printf("client: write csv: %v", err)
	}
	if err := metrics.WriteRTTSamplesCSV(*outPrefix+"-client-rtt.csv", rttLog.Snapshot()); err != nil {
		log.Printf("client: write rtt csv: %v", err)
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
				progress.Update(sent)
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
