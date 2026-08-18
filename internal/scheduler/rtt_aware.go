package scheduler

import "time"

func init() { Register("rtt-aware", func() Scheduler { return &rttAware{} }) }

// rttAware sends each chunk on the path with the lowest known RTT, falling
// back to round-robin for paths whose RTT hasn't been measured yet.
type rttAware struct {
	fallback roundRobin
}

func (s *rttAware) Assign(chunkSeq uint64, paths []PathInfo) []int {
	if len(paths) == 0 {
		return nil
	}
	best := -1
	var bestRTT time.Duration
	for _, p := range paths {
		if p.RTT <= 0 {
			continue
		}
		if best == -1 || p.RTT < bestRTT {
			best, bestRTT = p.Index, p.RTT
		}
	}
	if best == -1 {
		return s.fallback.Assign(chunkSeq, paths)
	}
	return []int{best}
}
