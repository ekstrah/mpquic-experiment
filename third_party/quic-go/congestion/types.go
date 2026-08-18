package congestion

import (
	"github.com/quic-go/quic-go/internal/monotime"
	"github.com/quic-go/quic-go/internal/protocol"
	"github.com/quic-go/quic-go/internal/utils"
)

// Type aliases re-exporting the internal types used by SendAlgorithm, so that
// congestion control implementations living outside this module can
// implement the interface without importing internal packages directly.
type (
	ByteCount       = protocol.ByteCount
	PacketNumber    = protocol.PacketNumber
	Time            = monotime.Time
	RTTStats        = utils.RTTStats
	ConnectionStats = utils.ConnectionStats
)
