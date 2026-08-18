package scheduler

import (
	"testing"
	"time"
)

func twoPaths() []PathInfo {
	return []PathInfo{{Index: 0}, {Index: 1}}
}

func TestRoundRobinCycles(t *testing.T) {
	s := &roundRobin{}
	paths := twoPaths()
	var got []int
	for seq := uint64(0); seq < 4; seq++ {
		got = append(got, s.Assign(seq, paths)...)
	}
	want := []int{0, 1, 0, 1}
	for i, v := range want {
		if got[i] != v {
			t.Fatalf("assignment %d = %d, want %d (full sequence: %v)", i, got[i], v, got)
		}
	}
}

func TestRedundantSendsAllPaths(t *testing.T) {
	got := (redundant{}).Assign(0, twoPaths())
	if len(got) != 2 {
		t.Fatalf("redundant.Assign returned %d paths, want 2", len(got))
	}
}

func TestRTTAwarePicksLowestRTT(t *testing.T) {
	s := &rttAware{}
	paths := []PathInfo{
		{Index: 0, RTT: 50 * time.Millisecond},
		{Index: 1, RTT: 10 * time.Millisecond},
	}
	got := s.Assign(0, paths)
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("Assign = %v, want [1] (lowest RTT path)", got)
	}
}

func TestRTTAwareFallsBackWhenRTTUnknown(t *testing.T) {
	s := &rttAware{}
	got := s.Assign(0, twoPaths()) // both RTT == 0 (unknown)
	if len(got) != 1 {
		t.Fatalf("Assign = %v, want exactly one path from the round-robin fallback", got)
	}
}

func TestRegistryLookup(t *testing.T) {
	for _, name := range []string{"roundrobin", "redundant", "rtt-aware"} {
		if _, err := Get(name); err != nil {
			t.Errorf("Get(%q): %v", name, err)
		}
	}
	if _, err := Get("nonexistent"); err == nil {
		t.Error("Get(\"nonexistent\") should have returned an error")
	}
}
