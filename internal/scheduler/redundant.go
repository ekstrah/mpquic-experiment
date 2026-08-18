package scheduler

func init() { Register("redundant", func() Scheduler { return redundant{} }) }

// redundant sends every chunk on every path, trading bandwidth for maximum
// reliability -- useful as an upper bound in reliability experiments.
type redundant struct{}

func (redundant) Assign(chunkSeq uint64, paths []PathInfo) []int {
	idxs := make([]int, len(paths))
	for i, p := range paths {
		idxs[i] = p.Index
	}
	return idxs
}
