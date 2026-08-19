package transfer

import (
	"sync"
	"time"
)

// BurstTracker verifies delivery completeness for continuous sessions
// (ControlHeader.TotalSize == 0), where there's no single known total size
// to allocate a Reassembler for. Unlike Reassembler it does not buffer
// payload bytes -- this is a reliability harness, not a data sink, so
// tracking "how many bytes of this burst have we seen" is enough; there's
// nothing to reassemble into.
//
// It's safe for concurrent use from multiple path goroutines, same as
// Reassembler.
type BurstTracker struct {
	mu     sync.Mutex
	bursts map[uint64]*burstState
	ttl    time.Duration
}

type burstState struct {
	bytesExpected uint64
	bytesReceived uint64
	seen          map[uint64]bool // by chunk seq, so duplicate/redundant-scheduler writes don't double count
	chunks        int
	corrupted     int
	startedAt     time.Time
	lastActivity  time.Time
	perPath       map[int]*PathBurstStats
}

// PathBurstStats is one path's contribution to a burst -- which path(s) a
// scheduler actually placed a burst's chunks on, visible per burst rather
// than only as a whole-session total.
type PathBurstStats struct {
	BytesReceived uint64
	Chunks        int
	Corrupted     int
}

// NewBurstTracker returns a tracker that expires a burst (see Expire) once
// it's gone ttl without a new chunk, so a burst that never completes (e.g.
// its connection dropped mid-burst) can't accumulate state forever.
func NewBurstTracker(ttl time.Duration) *BurstTracker {
	return &BurstTracker{bursts: make(map[uint64]*burstState), ttl: ttl}
}

// Write records one chunk of a burst. corrupted chunks are counted but not
// applied towards that burst's completion -- a good copy may still arrive
// on another path under a redundant scheduler. Returns whether this burst
// is now complete (every byte of BurstBytes accounted for).
func (t *BurstTracker) Write(c Chunk, corrupted bool, pathIndex int) (complete bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	b, ok := t.bursts[c.BurstID]
	if !ok {
		b = &burstState{bytesExpected: c.BurstBytes, seen: make(map[uint64]bool), startedAt: time.Now(), perPath: make(map[int]*PathBurstStats)}
		t.bursts[c.BurstID] = b
	}
	b.lastActivity = time.Now()
	b.chunks++
	p, ok := b.perPath[pathIndex]
	if !ok {
		p = &PathBurstStats{}
		b.perPath[pathIndex] = p
	}
	p.Chunks++
	if corrupted {
		b.corrupted++
		p.Corrupted++
		return b.bytesReceived >= b.bytesExpected
	}
	if !b.seen[c.Seq] {
		b.seen[c.Seq] = true
		b.bytesReceived += uint64(len(c.Data))
		p.BytesReceived += uint64(len(c.Data))
	}
	return b.bytesReceived >= b.bytesExpected
}

// BurstStats is a point-in-time snapshot of one burst's progress.
type BurstStats struct {
	BurstID       uint64
	BytesExpected uint64
	BytesReceived uint64
	Chunks        int
	Corrupted     int
	Complete      bool
	StartedAt     time.Time
	LastActivity  time.Time
	PerPath       map[int]PathBurstStats
}

func (b *burstState) stats(id uint64) BurstStats {
	perPath := make(map[int]PathBurstStats, len(b.perPath))
	for idx, p := range b.perPath {
		perPath[idx] = *p
	}
	return BurstStats{
		BurstID:       id,
		BytesExpected: b.bytesExpected,
		BytesReceived: b.bytesReceived,
		Chunks:        b.chunks,
		Corrupted:     b.corrupted,
		Complete:      b.bytesReceived >= b.bytesExpected,
		StartedAt:     b.startedAt,
		LastActivity:  b.lastActivity,
		PerPath:       perPath,
	}
}

// Stats returns the current snapshot for one burst, or ok=false if it's
// unknown (never seen, or already expired).
func (t *BurstTracker) Stats(burstID uint64) (BurstStats, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	b, ok := t.bursts[burstID]
	if !ok {
		return BurstStats{}, false
	}
	return b.stats(burstID), true
}

// Expire removes and returns stats for every burst that's gone idle longer
// than ttl (whether complete or not), so the caller can log/report a burst
// exactly once and bound this tracker's memory over a long-running session.
func (t *BurstTracker) Expire() []BurstStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	var expired []BurstStats
	now := time.Now()
	for id, b := range t.bursts {
		if now.Sub(b.lastActivity) < t.ttl {
			continue
		}
		expired = append(expired, b.stats(id))
		delete(t.bursts, id)
	}
	return expired
}

// ExpireAll force-flushes every burst regardless of idle time, for use at
// session/process shutdown when there's no more time to wait out the ttl.
func (t *BurstTracker) ExpireAll() []BurstStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	all := make([]BurstStats, 0, len(t.bursts))
	for id, b := range t.bursts {
		all = append(all, b.stats(id))
	}
	t.bursts = make(map[uint64]*burstState)
	return all
}
