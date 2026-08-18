package ccmodules

import (
	"github.com/quic-go/quic-go/congestion"
	"github.com/quic-go/quic-go/qlogwriter"
)

func init() {
	Register("cubic", func(rttStats *congestion.RTTStats, connStats *congestion.ConnectionStats, initialMaxDatagramSize congestion.ByteCount, qlogger qlogwriter.Recorder) congestion.SendAlgorithmWithDebugInfos {
		return congestion.NewCubicSender(congestion.DefaultClock{}, rttStats, connStats, initialMaxDatagramSize, true, qlogger)
	})
}
