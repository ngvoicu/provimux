// Package origin holds the process mark: set by Provimux's own main, so that an answer names its entry, and the record
// of what answers said is kept, only in the gateway as shipped (.github/provimux/design/answered-by.md, "Where an entry
// is named"). The package is internal: a program outside this module that embeds the gateway's SDK cannot import it,
// so it cannot set the mark. A source test of the layer (provimux_origin_callers_test.go) fails when a product file
// other than the one named calls it. ShippedGatewayForTests does nothing outside a test binary.
package origin

import (
	"sync/atomic"
	"testing"
)

// shippedGateway is the process mark: set once, by the gateway's own main, before anything else runs. Without it no
// answer names an entry, nothing is recorded, the record's file is neither read nor written, and the lists carry no
// `answered`.
var shippedGateway atomic.Bool

// MarkShippedGateway marks this process as one Provimux's own main started. Only cmd/server/main.go calls it (through
// logging.ProvimuxShippedGateway, the package that main already imports).
func MarkShippedGateway() { shippedGateway.Store(true) }

// ShippedGateway reports whether Provimux's own main started this process.
func ShippedGateway() bool { return shippedGateway.Load() }

// ShippedGatewayForTests sets the process mark to on, in a test binary only (a test that stands for the gateway as
// shipped), and returns what restores it. Outside a test binary it does nothing.
func ShippedGatewayForTests(on bool) (restore func()) {
	if !testing.Testing() {
		return func() {}
	}
	was := shippedGateway.Swap(on)
	return func() { shippedGateway.Store(was) }
}
