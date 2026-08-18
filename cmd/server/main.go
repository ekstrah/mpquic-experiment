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
	"sync"
	"time"

	quic "github.com/quic-go/quic-go"

	"mpquic-experiment/internal/metrics"
	"mpquic-experiment/internal/tlsconfig"
	"mpquic-experiment/internal/transfer"
)

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
		sess.reassembler.Write(chunk)
		sess.progress.Update(sess.reassembler.ReceivedBytes())
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
		Index:      pathIndex,
		LocalAddr:  conn.LocalAddr().String(),
		RemoteAddr: conn.RemoteAddr().String(),
		Bytes:      bytesReceived,
		Chunks:     chunks,
		Duration:   time.Since(pathStart),
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

type serverSession struct {
	id            transfer.SessionID
	outPrefix     string
	progressEvery time.Duration

	mu          sync.Mutex
	ctrl        *transfer.ControlHeader
	ready       chan struct{}
	readyClosed bool
	reassembler *transfer.Reassembler
	progress    *metrics.ProgressPrinter
	startTime   time.Time
	deliveryLog metrics.SampleLog[metrics.DeliverySample]

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
	s.reassembler = transfer.NewReassembler(ctrl.TotalSize, ctrl.ChunkSize)
	s.progress = metrics.NewProgressPrinter(ctrl.TotalSize, s.progressEvery)
	s.startTime = time.Now()
	s.wg.Add(int(ctrl.NumPaths))
	s.readyClosed = true
	close(s.ready)

	go func() {
		s.wg.Wait()
		s.finalize()
	}()
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
	ok := s.reassembler.VerifyHash(s.ctrl.Hash)

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
