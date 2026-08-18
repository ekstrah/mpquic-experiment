// Package metrics collects per-path and aggregate transfer stats and writes
// them out as JSON/CSV, plus optional live console progress.
package metrics

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

// PathStats is the per-path breakdown of one run.
type PathStats struct {
	Index           int           `json:"index"`
	LocalAddr       string        `json:"local_addr"`
	RemoteAddr      string        `json:"remote_addr"`
	Bytes           uint64        `json:"bytes"`
	Chunks          int           `json:"chunks"`
	ChunksCorrupted int           `json:"chunks_corrupted"`
	Duration        time.Duration `json:"duration_ns"`
	CC              string        `json:"congestion_control"`
}

// RunResult is the full record of one transfer, from either side.
type RunResult struct {
	Role           string        `json:"role"` // "client" or "server"
	SessionID      string        `json:"session_id"`
	TotalBytes     uint64        `json:"total_bytes"`
	ChunkSize      uint32        `json:"chunk_size"`
	Scheduler      string        `json:"scheduler"`
	StartTime      time.Time     `json:"start_time"`
	Duration       time.Duration `json:"duration_ns"`
	ThroughputMbps float64       `json:"throughput_mbps"`
	IntegrityOK    bool          `json:"integrity_ok"`
	Paths          []PathStats   `json:"paths"`
}

// Finalize fills in Duration and ThroughputMbps from StartTime and now.
func (r *RunResult) Finalize(now time.Time) {
	r.Duration = now.Sub(r.StartTime)
	if r.Duration > 0 {
		r.ThroughputMbps = float64(r.TotalBytes) * 8 / r.Duration.Seconds() / 1e6
	}
}

func (r RunResult) WriteJSON(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteCSV writes one row per path plus a final aggregate row (index -1).
func (r RunResult) WriteCSV(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()

	header := []string{"session_id", "role", "path_index", "local_addr", "remote_addr", "bytes", "chunks", "chunks_corrupted", "duration_ms", "congestion_control", "scheduler", "integrity_ok"}
	if err := w.Write(header); err != nil {
		return err
	}
	var corruptedTotal int
	for _, p := range r.Paths {
		corruptedTotal += p.ChunksCorrupted
		row := []string{
			r.SessionID, r.Role, strconv.Itoa(p.Index), p.LocalAddr, p.RemoteAddr,
			strconv.FormatUint(p.Bytes, 10), strconv.Itoa(p.Chunks), strconv.Itoa(p.ChunksCorrupted),
			strconv.FormatInt(p.Duration.Milliseconds(), 10), p.CC, r.Scheduler,
			strconv.FormatBool(r.IntegrityOK),
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	aggregate := []string{
		r.SessionID, r.Role, "-1", "", "",
		strconv.FormatUint(r.TotalBytes, 10), "", strconv.Itoa(corruptedTotal),
		strconv.FormatInt(r.Duration.Milliseconds(), 10), "", r.Scheduler,
		strconv.FormatBool(r.IntegrityOK),
	}
	return w.Write(aggregate)
}

// Print writes a human-readable summary to stdout.
func (r RunResult) Print() {
	fmt.Printf("=== %s run %s ===\n", r.Role, r.SessionID)
	integrity := "n/a (authoritative check is server-side)"
	if r.Role == "server" {
		integrity = fmt.Sprintf("%v", r.IntegrityOK)
	}
	fmt.Printf("total: %d bytes in %s (%.2f Mbps), integrity_ok=%s, scheduler=%s\n",
		r.TotalBytes, r.Duration, r.ThroughputMbps, integrity, r.Scheduler)
	for _, p := range r.Paths {
		fmt.Printf("  path %d [%s -> %s] cc=%s: %d bytes, %d chunks (%d corrupted), %s\n",
			p.Index, p.LocalAddr, p.RemoteAddr, p.CC, p.Bytes, p.Chunks, p.ChunksCorrupted, p.Duration)
	}
}

// RTTSample is one periodic RTT observation for a single path, taken from
// the client side (RTT is measured from the sender's ACK-clocked view).
type RTTSample struct {
	SessionID     string
	PathIndex     int
	TMs           int64
	LatestRTTMs   float64
	SmoothedRTTMs float64
}

// DeliverySample is one chunk-delivery event: bytes arriving on a path,
// taken from the server (receiver) side so it reflects actual goodput
// rather than offered load.
type DeliverySample struct {
	SessionID string
	PathIndex int
	Seq       uint64
	TMs       int64
	Bytes     int
}

// SampleLog is a concurrency-safe append-only collector for samples
// gathered from multiple per-path goroutines over the life of a transfer.
type SampleLog[T any] struct {
	mu   sync.Mutex
	rows []T
}

func (l *SampleLog[T]) Add(v T) {
	l.mu.Lock()
	l.rows = append(l.rows, v)
	l.mu.Unlock()
}

// Snapshot returns a copy of the samples collected so far.
func (l *SampleLog[T]) Snapshot() []T {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]T, len(l.rows))
	copy(out, l.rows)
	return out
}

func WriteRTTSamplesCSV(path string, samples []RTTSample) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"session_id", "path_index", "t_ms", "latest_rtt_ms", "smoothed_rtt_ms"}); err != nil {
		return err
	}
	for _, s := range samples {
		row := []string{
			s.SessionID, strconv.Itoa(s.PathIndex), strconv.FormatInt(s.TMs, 10),
			strconv.FormatFloat(s.LatestRTTMs, 'f', 3, 64), strconv.FormatFloat(s.SmoothedRTTMs, 'f', 3, 64),
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	return nil
}

func WriteDeliverySamplesCSV(path string, samples []DeliverySample) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"session_id", "path_index", "seq", "t_ms", "bytes"}); err != nil {
		return err
	}
	for _, s := range samples {
		row := []string{
			s.SessionID, strconv.Itoa(s.PathIndex), strconv.FormatUint(s.Seq, 10),
			strconv.FormatInt(s.TMs, 10), strconv.Itoa(s.Bytes),
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	return nil
}

// ProgressPrinter periodically prints bytes-so-far / total to stdout. Call
// Update from the hot path; a background ticker drains it at most once per
// interval so printing never blocks the transfer.
type ProgressPrinter struct {
	total   uint64
	current chan uint64
	done    chan struct{}
}

func NewProgressPrinter(total uint64, interval time.Duration) *ProgressPrinter {
	p := &ProgressPrinter{total: total, current: make(chan uint64, 1), done: make(chan struct{})}
	go p.loop(interval)
	return p
}

func (p *ProgressPrinter) loop(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	var last uint64
	for {
		select {
		case v := <-p.current:
			last = v
		case <-t.C:
			pct := float64(last) / float64(p.total) * 100
			fmt.Printf("\rprogress: %d/%d bytes (%.1f%%)", last, p.total, pct)
		case <-p.done:
			fmt.Printf("\rprogress: %d/%d bytes (100%%)\n", p.total, p.total)
			return
		}
	}
}

// Update reports the new cumulative byte count. Non-blocking.
func (p *ProgressPrinter) Update(bytesSoFar uint64) {
	select {
	case p.current <- bytesSoFar:
	default:
	}
}

func (p *ProgressPrinter) Close() { close(p.done) }
