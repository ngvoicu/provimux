package logging

import "github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/origin"

// ProvimuxShippedGateway marks this process as the gateway as shipped (the lead's amendment of 2026-11-27 (2), part 3).
// Only cmd/server/main.go calls it, first thing in main; this package is internal, so a program in another Go module
// cannot import it, unless it gives its module a path inside this module's, as a fork that edits main could (written
// limit 50, answered-by.md part (5)).
func ProvimuxShippedGateway() { origin.MarkShippedGateway() }

// provimuxRecording reports whether answers are recorded: models.answered-by on, in the gateway as shipped.
func provimuxRecording() bool { return ProvimuxAnsweredBy() && origin.ShippedGateway() }
