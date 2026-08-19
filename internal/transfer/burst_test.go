package transfer

import (
	"testing"
	"time"
)

func TestBurstTrackerCompletesOnFullBytes(t *testing.T) {
	tr := NewBurstTracker(time.Minute)
	data := make([]byte, 3000)
	chunks := Split(data, 1000, 1, uint64(len(data)))

	var complete bool
	for i, c := range chunks {
		complete = tr.Write(c, false, 0)
		if i < len(chunks)-1 && complete {
			t.Fatalf("burst reported complete after chunk %d, before the last one arrived", i)
		}
	}
	if !complete {
		t.Fatal("burst never reported complete after every chunk arrived")
	}

	stats, ok := tr.Stats(1)
	if !ok {
		t.Fatal("Stats: burst 1 not found")
	}
	if !stats.Complete || stats.BytesReceived != uint64(len(data)) || stats.Chunks != len(chunks) || stats.Corrupted != 0 {
		t.Fatalf("Stats = %+v, want Complete=true BytesReceived=%d Chunks=%d Corrupted=0", stats, len(data), len(chunks))
	}
}

func TestBurstTrackerExcludesCorruptedChunks(t *testing.T) {
	tr := NewBurstTracker(time.Minute)
	data := make([]byte, 2000)
	chunks := Split(data, 1000, 2, uint64(len(data)))

	tr.Write(chunks[0], true, 0) // corrupted: counted, but not applied towards completion
	complete := tr.Write(chunks[1], false, 0)
	if complete {
		t.Fatal("burst reported complete with only one good chunk out of two expected bytes' worth")
	}

	stats, ok := tr.Stats(2)
	if !ok {
		t.Fatal("Stats: burst 2 not found")
	}
	if stats.Corrupted != 1 {
		t.Fatalf("Corrupted = %d, want 1", stats.Corrupted)
	}
	if stats.BytesReceived != 1000 {
		t.Fatalf("BytesReceived = %d, want 1000 (corrupted chunk's bytes must not count)", stats.BytesReceived)
	}
	if stats.Complete {
		t.Fatal("burst should not be complete: corrupted chunk's bytes were never actually received")
	}

	// A good copy of the corrupted chunk (e.g. arriving on another path
	// under a redundant scheduler) should still complete the burst.
	complete = tr.Write(chunks[0], false, 0)
	if !complete {
		t.Fatal("burst should complete once a good copy of the previously-corrupted chunk arrives")
	}
}

func TestBurstTrackerDuplicateWritesDontDoubleCount(t *testing.T) {
	tr := NewBurstTracker(time.Minute)
	data := make([]byte, 1000)
	chunks := Split(data, 1000, 3, uint64(len(data)))

	tr.Write(chunks[0], false, 0)
	tr.Write(chunks[0], false, 0) // duplicate, e.g. from a redundant scheduler

	stats, _ := tr.Stats(3)
	if stats.BytesReceived != 1000 {
		t.Fatalf("BytesReceived = %d, want 1000 (duplicate must not double-count)", stats.BytesReceived)
	}
	if stats.Chunks != 2 {
		t.Fatalf("Chunks = %d, want 2 (both writes should still count as arrivals)", stats.Chunks)
	}
}

func TestBurstTrackerExpireOnlyIdleBursts(t *testing.T) {
	tr := NewBurstTracker(20 * time.Millisecond)
	data := make([]byte, 500)
	chunks := Split(data, 500, 4, uint64(len(data)))
	tr.Write(chunks[0], false, 0) // burst 4: complete immediately, but not yet idle

	if expired := tr.Expire(); len(expired) != 0 {
		t.Fatalf("Expire() = %v, want none yet (burst hasn't gone idle)", expired)
	}

	time.Sleep(30 * time.Millisecond)
	expired := tr.Expire()
	if len(expired) != 1 || expired[0].BurstID != 4 {
		t.Fatalf("Expire() = %+v, want exactly burst 4", expired)
	}
	if !expired[0].Complete {
		t.Fatal("expired burst 4 should be reported complete")
	}
	if _, ok := tr.Stats(4); ok {
		t.Fatal("Stats: expired burst should have been forgotten")
	}
}

func TestBurstTrackerPerPathBreakdown(t *testing.T) {
	tr := NewBurstTracker(time.Minute)
	data := make([]byte, 3000)
	chunks := Split(data, 1000, 7, uint64(len(data)))

	tr.Write(chunks[0], false, 0) // path 0: one good chunk
	tr.Write(chunks[1], true, 1)  // path 1: one corrupted chunk
	tr.Write(chunks[1], false, 1) // path 1: good copy of the same chunk (e.g. redundant scheduler)
	tr.Write(chunks[2], false, 0) // path 0: second good chunk

	stats, ok := tr.Stats(7)
	if !ok {
		t.Fatal("Stats: burst 7 not found")
	}
	if len(stats.PerPath) != 2 {
		t.Fatalf("PerPath has %d entries, want 2 (paths 0 and 1)", len(stats.PerPath))
	}
	p0, p1 := stats.PerPath[0], stats.PerPath[1]
	if p0.BytesReceived != 2000 || p0.Chunks != 2 || p0.Corrupted != 0 {
		t.Fatalf("path 0 = %+v, want BytesReceived=2000 Chunks=2 Corrupted=0", p0)
	}
	if p1.BytesReceived != 1000 || p1.Chunks != 2 || p1.Corrupted != 1 {
		t.Fatalf("path 1 = %+v, want BytesReceived=1000 Chunks=2 Corrupted=1", p1)
	}
	// Whole-burst totals must still match the sum across paths.
	if stats.BytesReceived != p0.BytesReceived+p1.BytesReceived {
		t.Fatalf("aggregate BytesReceived=%d != sum of per-path (%d)", stats.BytesReceived, p0.BytesReceived+p1.BytesReceived)
	}
}

func TestBurstTrackerExpireAllIgnoresTTL(t *testing.T) {
	tr := NewBurstTracker(time.Hour) // long enough that Expire() alone would find nothing
	tr.Write(Split(make([]byte, 100), 100, 5, 100)[0], false, 0)
	tr.Write(Split(make([]byte, 50), 100, 6, 999)[0], false, 0) // burst 6 deliberately left incomplete (only 50 of 999 expected bytes)

	all := tr.ExpireAll()
	if len(all) != 2 {
		t.Fatalf("ExpireAll() returned %d bursts, want 2", len(all))
	}
	if _, ok := tr.Stats(5); ok {
		t.Fatal("ExpireAll should have forgotten every burst")
	}
}
