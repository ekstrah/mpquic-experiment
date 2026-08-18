// Package scheduler decides which path(s) each data chunk is sent on.
package scheduler

import (
	"fmt"
	"sort"
	"time"
)

// PathInfo is what a scheduler knows about one path when assigning a chunk.
type PathInfo struct {
	Index int
	// RTT is the path's last known round-trip time, or 0 if unknown.
	RTT time.Duration
}

// Scheduler assigns chunk chunkSeq to one or more of the given paths.
// Returning more than one path index means the chunk is sent redundantly on
// all of them.
type Scheduler interface {
	Assign(chunkSeq uint64, paths []PathInfo) []int
}

// Factory constructs a fresh Scheduler instance (schedulers carry per-transfer
// state such as round-robin counters, so each transfer gets its own).
type Factory func() Scheduler

var registry = map[string]Factory{}

func Register(name string, f Factory) { registry[name] = f }

func Get(name string) (Factory, error) {
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown scheduler %q (available: %v)", name, Names())
	}
	return f, nil
}

func Names() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
