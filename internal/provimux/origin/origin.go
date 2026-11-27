// Package origin holds two of the positive marks the gateway as shipped sets on what it makes itself, so that an
// answer names its entry only where Provimux made every part (.github/provimux/design/answered-by.md, the lead's
// amendment of 2026-11-27 (2), rules A.2 and A.3):
//
//   - the transports the gateway's own per-credential transport provider built, recognised by identity;
//   - the credentials the gateway's own loader parsed from the auth folder, a store record or the configuration, marked
//     on the credential where it is made (sdk/cliproxy/auth keeps the mark in an unexported field).
//
// The package is internal: a program outside this module that embeds the gateway's SDK cannot import it, so it cannot
// set either mark. Inside the module only the places named on each function call them; a source test of the layer
// (provimux_origin_callers_test.go) fails when another product file does. The two ...ForTests functions do nothing
// outside a test binary (testing.Testing), so no product code can use them even by mistake.
package origin

import (
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

// gatewayTransports holds every transport the gateway's own per-credential transport provider returned (keyed by the
// transport itself). The provider caches its transports per proxy URL, so the set stays as small as that cache.
var gatewayTransports sync.Map

// goDefaultTransport is http.DefaultTransport as it was when this package was initialised: before any package that
// imports the gateway could replace it.
var goDefaultTransport = http.DefaultTransport

// NoteGatewayTransport records rt as one the gateway's own per-credential transport provider built (a pointer, as all
// of them are). Only that provider (sdk/cliproxy, provimux_origin.go) calls it.
func NoteGatewayTransport(rt http.RoundTripper) {
	if comparable(rt) {
		gatewayTransports.Store(rt, struct{}{})
	}
}

// GatewayTransport reports whether rt is a transport the gateway's own per-credential provider built (or a test's
// stand-in registered by StandInForTests in a test binary). A nil rt is not one: the caller decides what "none" means.
func GatewayTransport(rt any) bool {
	if !comparable(rt) {
		return false
	}
	_, ok := gatewayTransports.Load(rt)
	return ok
}

// DefaultTransportUnchanged reports whether http.DefaultTransport is still the transport Go made, as it was when this
// package was initialised.
func DefaultTransportUnchanged() bool { return http.DefaultTransport == goDefaultTransport }

// StandInForTests records rt as if the gateway's per-credential provider had built it, in a test binary only: a test's
// stand-in vendor goes through the same recognition as the gateway's own transport. Outside a test binary it does
// nothing.
func StandInForTests(rt http.RoundTripper) {
	if testing.Testing() {
		NoteGatewayTransport(rt)
	}
}

// comparable reports whether v can be recognised by identity: a pointer. A value of another kind (a struct that holds
// a function, say) is never one of the gateway's own, and is never hashed.
func comparable(v any) bool {
	return v != nil && reflect.TypeOf(v).Kind() == reflect.Pointer
}

// loadedMark sets the loader's mark on a credential; sdk/cliproxy/auth installs it once, at its initialisation.
var loadedMark atomic.Pointer[func(any)]

// InstallLoadedMark installs the function that sets the loader's mark. The first installation holds: sdk/cliproxy/auth
// makes it while the program initialises, before any code that imports the gateway runs.
func InstallLoadedMark(mark func(any)) {
	if mark != nil {
		loadedMark.CompareAndSwap(nil, &mark)
	}
}

// MarkLoaded sets the loader's mark on each credential (*auth.Auth). Only the gateway's own parsers call it: the auth
// file synthesizer (a login parsed natively, never one a plugin's parser claimed), the configuration's synthesizer,
// and the four token stores where they read a record.
func MarkLoaded(credentials ...any) {
	mark := loadedMark.Load()
	if mark == nil {
		return
	}
	for _, credential := range credentials {
		(*mark)(credential)
	}
}

// MarkLoadedForTests is MarkLoaded in a test binary only: a test that registers a login in memory says that the
// gateway's loader made it. Outside a test binary it does nothing.
func MarkLoadedForTests(credentials ...any) {
	if testing.Testing() {
		MarkLoaded(credentials...)
	}
}
