package ccmodules

import (
	"sync"

	"github.com/quic-go/quic-go/congestion"
	"github.com/quic-go/quic-go/qlogwriter"
)

func init() {
	Register("fixed", func(rttStats *congestion.RTTStats, connStats *congestion.ConnectionStats, initialMaxDatagramSize congestion.ByteCount, qlogger qlogwriter.Recorder) congestion.SendAlgorithmWithDebugInfos {
		return &fixedWindowSender{window: 64 * initialMaxDatagramSize}
	})
}

// fixedWindowSender is a template for a custom congestion control module:
// a constant congestion window, no slow start, no reaction to loss. Copy
// this file, rename the type, and replace the logic in OnPacketAcked /
// OnCongestionEvent / GetCongestionWindow with a real algorithm (e.g. AIMD).
type fixedWindowSender struct {
	mu         sync.Mutex
	window     congestion.ByteCount
	inRecovery bool
}

var _ congestion.SendAlgorithmWithDebugInfos = (*fixedWindowSender)(nil)

func (f *fixedWindowSender) TimeUntilSend(bytesInFlight congestion.ByteCount) congestion.Time {
	return 0 // no pacing: send immediately whenever CanSend allows it
}

func (f *fixedWindowSender) HasPacingBudget(now congestion.Time) bool { return true }

func (f *fixedWindowSender) OnPacketSent(sentTime congestion.Time, bytesInFlight congestion.ByteCount, packetNumber congestion.PacketNumber, bytes congestion.ByteCount, isRetransmittable bool) {
}

func (f *fixedWindowSender) CanSend(bytesInFlight congestion.ByteCount) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return bytesInFlight < f.window
}

func (f *fixedWindowSender) MaybeExitSlowStart() {}

func (f *fixedWindowSender) OnPacketAcked(number congestion.PacketNumber, ackedBytes congestion.ByteCount, priorInFlight congestion.ByteCount, eventTime congestion.Time) {
}

func (f *fixedWindowSender) OnCongestionEvent(number congestion.PacketNumber, lostBytes congestion.ByteCount, priorInFlight congestion.ByteCount) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inRecovery = true
}

func (f *fixedWindowSender) OnRetransmissionTimeout(packetsRetransmitted bool) {}

func (f *fixedWindowSender) SetMaxDatagramSize(s congestion.ByteCount) {}

func (f *fixedWindowSender) InSlowStart() bool { return false }

func (f *fixedWindowSender) InRecovery() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inRecovery
}

func (f *fixedWindowSender) GetCongestionWindow() congestion.ByteCount {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.window
}
