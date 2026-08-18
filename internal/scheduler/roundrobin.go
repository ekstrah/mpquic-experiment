package scheduler

func init() { Register("roundrobin", func() Scheduler { return &roundRobin{} }) }

type roundRobin struct{ n uint64 }

func (r *roundRobin) Assign(chunkSeq uint64, paths []PathInfo) []int {
	if len(paths) == 0 {
		return nil
	}
	idx := r.n % uint64(len(paths))
	r.n++
	return []int{paths[idx].Index}
}
