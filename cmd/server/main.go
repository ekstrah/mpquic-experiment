// Command server accepts one or more QUIC connections (paths) per session,
// correlates them by session ID, reassembles the striped payload, and
// verifies its integrity.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	quic "github.com/quic-go/quic-go"

	"mpquic-experiment/internal/metrics"
	"mpquic-experiment/internal/tlsconfig"
	"mpquic-experiment/internal/transfer"
)

// burstTTL bounds how long a continuous session's BurstTracker will wait
// for more chunks of a given burst before treating it as abandoned
// (connection dropped mid-burst) and reporting it incomplete.
const burstTTL = 5 * time.Second

// burstSweepInterval is how often a continuous session checks for bursts
// that have gone idle past burstTTL, and rewrites that session's bursts.csv
// with whatever's accumulated so far.
const burstSweepInterval = time.Second

func main() {
	listenAddr := flag.String("listen", ":4433", "UDP address to listen on")
	outPrefix := flag.String("out", "server-results", "output file prefix for -results.json / -results.csv")
	progressEvery := flag.Duration("progress-interval", time.Second, "console progress print interval")
	flag.Parse()

	tlsConf, err := tlsconfig.ServerConfig()
	if err != nil {
		log.Fatalf("server: %v", err)
	}
	ln, err := quic.ListenAddr(*listenAddr, tlsConf, &quic.Config{})
	if err != nil {
		log.Fatalf("server: listen: %v", err)
	}
	defer ln.Close()
	log.Printf("server: listening on %s", ln.Addr())

	registry := newSessionRegistry(*progressEvery)

	// A continuous session has no natural "every path hit EOF" moment to
	// finalize at while the client keeps streaming, so an operator killing
	// the server needs its own graceful path: flush whatever's accumulated
	// for every still-running continuous session before the process exits.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Printf("server: shutting down, flushing active sessions")
		registry.shutdownAll()
		os.Exit(0)
	}()

	for {
		conn, err := ln.Accept(context.Background())
		if err != nil {
			log.Printf("server: accept: %v", err)
			return
		}
		go handleConn(conn, registry, *outPrefix)
	}
}

func handleConn(conn *quic.Conn, reg *sessionRegistry, outPrefix string) {
	// closeNow stays true (connection is closed as soon as this function
	// returns) except on the clean-completion path below, where closing
	// immediately after writing the drain ack would race CloseWithError
	// against that write actually reaching the client.
	closeNow := true
	defer func() {
		if closeNow {
			conn.CloseWithError(0, "")
		}
	}()

	stream, err := conn.AcceptStream(context.Background())
	if err != nil {
		log.Printf("server: accept stream from %s: %v", conn.RemoteAddr(), err)
		return
	}
	ctrl, hello, err := transfer.ReadPreamble(stream)
	if err != nil {
		log.Printf("server: read preamble from %s: %v", conn.RemoteAddr(), err)
		return
	}

	var sessID transfer.SessionID
	var pathIndex int
	if ctrl != nil {
		sessID, pathIndex = ctrl.SessionID, 0
	} else {
		sessID, pathIndex = hello.SessionID, int(hello.PathIndex)
	}
	sess := reg.getOrCreate(sessID, outPrefix)

	if ctrl != nil {
		sess.start(*ctrl)
	}
	if !sess.waitReady(context.Background()) {
		log.Printf("server: session %x: timed out waiting for control header", sessID)
		return
	}

	var bytesReceived uint64
	var chunks int
	var corrupted int
	var readErr error
	pathStart := time.Now()
	for {
		chunk, err := transfer.ReadChunk(stream)
		if err != nil {
			readErr = err
			if !errors.Is(err, io.EOF) {
				log.Printf("server: session %x path %d: read chunk: %v", sessID, pathIndex, err)
			}
			break
		}
		chunks++
		bytesReceived += uint64(len(chunk.Data))
		corrupt := !chunk.VerifyChecksum()
		if corrupt {
			corrupted++
			log.Printf("server: session %x path %d: chunk %d failed checksum, discarding", sessID, pathIndex, chunk.Seq)
		}
		switch {
		case sess.burstTracker != nil:
			// Continuous session: route to the per-burst tracker regardless
			// of corrupt (it counts corrupted chunks itself), since there's
			// no session-wide reassembler in this mode.
			sess.burstTracker.Write(chunk, corrupt, pathIndex)
		case !corrupt:
			sess.reassembler.Write(chunk)
		}
		if corrupt {
			continue // a good copy may still arrive (e.g. via a redundant scheduler)
		}
		if sess.progress != nil {
			sess.progress.Update(sess.reassembler.ReceivedBytes())
		}
		sess.deliveryLog.Add(metrics.DeliverySample{
			SessionID: fmt.Sprintf("%x", sessID),
			PathIndex: pathIndex,
			Seq:       chunk.Seq,
			TMs:       time.Since(sess.startTime).Milliseconds(),
			Bytes:     len(chunk.Data),
		})
	}
	log.Printf("server: session %x path %d done: %d bytes, smoothed_rtt=%s", sessID, pathIndex, bytesReceived, conn.ConnectionStats().SmoothedRTT)

	sess.pathDone(metrics.PathStats{
		Index:           pathIndex,
		LocalAddr:       conn.LocalAddr().String(),
		RemoteAddr:      conn.RemoteAddr().String(),
		Bytes:           bytesReceived,
		Chunks:          chunks,
		ChunksCorrupted: corrupted,
		Duration:        time.Since(pathStart),
	})

	// A clean EOF here is proof-positive that every byte the client wrote on
	// this stream, up to and including the FIN, was delivered in order (QUIC
	// streams are strictly ordered and reliable). Tell the client so it can
	// safely close this path's connection without racing a still-in-flight
	// tail write against a force-close. Leave the connection open afterward
	// (closeNow stays false) so this ack isn't itself abandoned mid-flight;
	// the client's own close (or, failing that, the idle timeout) tears it
	// down once the ack has had a chance to land.
	if errors.Is(readErr, io.EOF) {
		if _, err := stream.Write([]byte{1}); err != nil {
			log.Printf("server: session %x path %d: write drain ack: %v", sessID, pathIndex, err)
		} else {
			closeNow = false
		}
	}
}

// sessionRegistry tracks in-flight and finished sessions.
type sessionRegistry struct {
	mu            sync.Mutex
	sessions      map[transfer.SessionID]*serverSession
	progressEvery time.Duration
}

func newSessionRegistry(progressEvery time.Duration) *sessionRegistry {
	return &sessionRegistry{sessions: map[transfer.SessionID]*serverSession{}, progressEvery: progressEvery}
}

func (r *sessionRegistry) getOrCreate(id transfer.SessionID, outPrefix string) *serverSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.sessions[id]; ok {
		return s
	}
	s := &serverSession{id: id, ready: make(chan struct{}), outPrefix: outPrefix, progressEvery: r.progressEvery}
	r.sessions[id] = s
	return s
}

// shutdownAll force-flushes every still-active continuous session, for use
// when the server process itself is being interrupted mid-session (the
// client may still be streaming). One-shot sessions have no partial-result
// concept and are left alone -- the process exiting is no different from
// today's behavior for them.
func (r *sessionRegistry) shutdownAll() {
	r.mu.Lock()
	sessions := make([]*serverSession, 0, len(r.sessions))
	for _, s := range r.sessions {
		sessions = append(sessions, s)
	}
	r.mu.Unlock()
	for _, s := range sessions {
		s.shutdownContinuous()
	}
}

type serverSession struct {
	id            transfer.SessionID
	outPrefix     string
	progressEvery time.Duration

	mu           sync.Mutex
	ctrl         *transfer.ControlHeader
	ready        chan struct{}
	readyClosed  bool
	reassembler  *transfer.Reassembler  // one-shot (-size) sessions only
	burstTracker *transfer.BurstTracker // continuous sessions only
	progress     *metrics.ProgressPrinter
	startTime    time.Time
	deliveryLog  metrics.SampleLog[metrics.DeliverySample]

	burstLog     metrics.SampleLog[metrics.BurstSample]
	stopSweep    chan struct{}
	stopSweepOne sync.Once

	wg          sync.WaitGroup
	pathResults []metrics.PathStats
	pathMu      sync.Mutex
}

func (s *serverSession) start(ctrl transfer.ControlHeader) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readyClosed {
		return
	}
	s.ctrl = &ctrl
	s.startTime = time.Now()
	if ctrl.TotalSize == 0 {
		s.burstTracker = transfer.NewBurstTracker(burstTTL)
		s.stopSweep = make(chan struct{})
		go s.burstSweepLoop()
	} else {
		s.reassembler = transfer.NewReassembler(ctrl.TotalSize, ctrl.ChunkSize)
		s.progress = metrics.NewProgressPrinter(ctrl.TotalSize, s.progressEvery)
	}
	s.wg.Add(int(ctrl.NumPaths))
	s.readyClosed = true
	close(s.ready)

	go func() {
		s.wg.Wait()
		if s.burstTracker != nil {
			s.shutdownContinuous()
		} else {
			s.finalize()
		}
	}()
}

// burstSweepLoop periodically reports (and forgets) any burst that's gone
// idle past burstTTL, and rewrites this session's bursts.csv with whatever
// has accumulated so far -- a continuous session may run indefinitely, so
// results can't wait for a single "the end" the way a one-shot session's do.
func (s *serverSession) burstSweepLoop() {
	ticker := time.NewTicker(burstSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.recordBursts(s.burstTracker.Expire())
		case <-s.stopSweep:
			return
		}
	}
}

func (s *serverSession) recordBursts(stats []transfer.BurstStats) {
	if len(stats) == 0 {
		return
	}
	sessIDStr := fmt.Sprintf("%x", s.id)
	for _, b := range stats {
		startMs, endMs := b.StartedAt.Sub(s.startTime).Milliseconds(), b.LastActivity.Sub(s.startTime).Milliseconds()
		s.burstLog.Add(metrics.BurstSample{
			SessionID: sessIDStr, Role: "server", BurstID: b.BurstID, PathIndex: -1,
			BytesExpected: b.BytesExpected, BytesReceived: b.BytesReceived,
			Chunks: b.Chunks, Corrupted: b.Corrupted, Complete: b.Complete,
			StartMs: startMs, EndMs: endMs,
		})
		for idx, p := range b.PerPath {
			s.burstLog.Add(metrics.BurstSample{
				SessionID: sessIDStr, Role: "server", BurstID: b.BurstID, PathIndex: idx,
				BytesExpected: b.BytesExpected, BytesReceived: p.BytesReceived,
				Chunks: p.Chunks, Corrupted: p.Corrupted, Complete: p.BytesReceived >= b.BytesExpected,
				StartMs: startMs, EndMs: endMs,
			})
		}
		if !b.Complete {
			log.Printf("server: session %x burst %d incomplete: %d/%d bytes (%d corrupted)",
				s.id, b.BurstID, b.BytesReceived, b.BytesExpected, b.Corrupted)
		}
	}
	if err := metrics.WriteBurstSamplesCSV(s.outPrefix+"-bursts.csv", s.burstLog.Snapshot()); err != nil {
		log.Printf("server: session %x: write bursts csv: %v", s.id, err)
	}
}

// shutdownContinuous stops the sweep loop and force-flushes every
// remaining burst, whether complete or not. Safe to call more than once
// (e.g. once from wg.Wait() finishing, once from process shutdown racing
// it) -- only the first call does anything.
func (s *serverSession) shutdownContinuous() {
	if s.burstTracker == nil {
		return
	}
	s.stopSweepOne.Do(func() { close(s.stopSweep) })
	s.recordBursts(s.burstTracker.ExpireAll())
}

func (s *serverSession) waitReady(ctx context.Context) bool {
	select {
	case <-s.ready:
		return true
	case <-time.After(60 * time.Second):
		return false
	case <-ctx.Done():
		return false
	}
}

func (s *serverSession) pathDone(p metrics.PathStats) {
	s.pathMu.Lock()
	s.pathResults = append(s.pathResults, p)
	s.pathMu.Unlock()
	s.wg.Done()
}

func (s *serverSession) finalize() {
	s.progress.Close()
	var corrupted int
	for _, p := range s.pathResults {
		corrupted += p.ChunksCorrupted
	}
	ok := corrupted == 0 && s.reassembler.Complete()

	result := metrics.RunResult{
		Role:        "server",
		SessionID:   fmt.Sprintf("%x", s.id),
		TotalBytes:  s.ctrl.TotalSize,
		ChunkSize:   s.ctrl.ChunkSize,
		Scheduler:   s.ctrl.Scheduler,
		StartTime:   s.startTime,
		IntegrityOK: ok,
		Paths:       s.pathResults,
	}
	result.Finalize(time.Now())
	result.Print()
	if err := result.WriteJSON(s.outPrefix + "-results.json"); err != nil {
		log.Printf("server: write json: %v", err)
	}
	if err := result.WriteCSV(s.outPrefix + "-results.csv"); err != nil {
		log.Printf("server: write csv: %v", err)
	}
	if err := metrics.WriteDeliverySamplesCSV(s.outPrefix+"-server-delivery.csv", s.deliveryLog.Snapshot()); err != nil {
		log.Printf("server: write delivery csv: %v", err)
	}
}
