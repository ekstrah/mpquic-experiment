// Package ccmodules is the pluggable congestion control registry for the
// experiment platform. Each algorithm registers a Factory under a name;
// cmd/client and cmd/server select one per path via the -cc flag.
package ccmodules

import (
	"fmt"
	"sort"

	"github.com/quic-go/quic-go/congestion"
	"github.com/quic-go/quic-go/qlogwriter"
)

// Factory constructs a congestion controller for one connection. This is a
// type ALIAS (note the "="), not a new named type: quic.Config's
// CongestionControlFactory field has the identical underlying function type
// but lives in an internal package we can't name here, so Get's return
// value must stay structurally/unnamed-assignable to it rather than becoming
// its own distinct named type.
type Factory = func(rttStats *congestion.RTTStats, connStats *congestion.ConnectionStats, initialMaxDatagramSize congestion.ByteCount, qlogger qlogwriter.Recorder) congestion.SendAlgorithmWithDebugInfos

var registry = map[string]Factory{}

// Register adds a named congestion control algorithm. Call from an init()
// in the algorithm's file.
func Register(name string, f Factory) {
	registry[name] = f
}

// Get looks up a registered algorithm by name.
func Get(name string) (Factory, error) {
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown congestion control module %q (available: %v)", name, Names())
	}
	return f, nil
}

// Names lists all registered algorithm names, sorted.
func Names() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
