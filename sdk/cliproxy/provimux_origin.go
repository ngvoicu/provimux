package cliproxy

import (
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/origin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: the gateway's own per-credential transports, recognised by identity (.github/provimux/design/answered-by.md,
// the lead's amendment of 2026-11-27 (2), rule A.2). The builder hands the manager its default transport provider
// through provimuxGatewayTransports, which records each transport that provider returns. An answer names an entry
// only when the per-credential transport its client was built over is one of these, or there is none. A provider an
// embedding sets on the manager later (SetRoundTripperProvider) is not wrapped: its transports are not recognised, and
// its answers name no entry.

// provimuxGatewayProvider is the default per-credential transport provider, recording what it returns.
type provimuxGatewayProvider struct{ inner coreauth.RoundTripperProvider }

// provimuxGatewayTransports wraps the builder's own default transport provider. Only builder.go calls it.
func provimuxGatewayTransports(inner coreauth.RoundTripperProvider) coreauth.RoundTripperProvider {
	return &provimuxGatewayProvider{inner: inner}
}

// RoundTripperFor implements coreauth.RoundTripperProvider: the inner provider's transport, recorded as the gateway's.
func (p *provimuxGatewayProvider) RoundTripperFor(auth *coreauth.Auth) http.RoundTripper {
	rt := p.inner.RoundTripperFor(auth)
	if rt != nil {
		origin.NoteGatewayTransport(rt)
	}
	return rt
}
