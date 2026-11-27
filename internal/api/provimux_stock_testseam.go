package api

import (
	"net/http"
	"testing"
)

// ProvimuxStockServerForTests returns, in a test binary only, the HTTP server NewServer built (the gateway's stock
// output route, rule B.1 of the lead's amendment of 2026-11-27 (2)), so that a test serves its answers exactly as
// Start would, on a listener of its own. Outside a test binary it returns nil.
func (s *Server) ProvimuxStockServerForTests() *http.Server {
	if s == nil || !testing.Testing() {
		return nil
	}
	return s.server
}
