package buildinfo

import "strings"

// Provimux distribution metadata. It is kept apart from Version on purpose:
// Version is the CLIProxyAPI base version and is sent to AI providers (Kimi
// User-Agent and X-Msh-Version, the Claude fallback User-Agent), so it must stay
// identical to stock CLIProxyAPI. ProvimuxVersion is only shown locally.
var (
	// ProvimuxVersion is the Provimux release version, e.g. "8.0.77-pmx.1".
	// Overridden via ldflags:
	// -X github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo.ProvimuxVersion=<version>
	ProvimuxVersion = "dev"
)

// ProvimuxRepo is the GitHub repository Provimux releases are published from.
const ProvimuxRepo = "ngvoicu/provimux"

// ProvimuxVersionSet reports whether a real Provimux version was baked in.
func ProvimuxVersionSet() bool {
	v := strings.TrimSpace(ProvimuxVersion)
	return v != "" && v != "dev"
}

// DisplayVersion returns the Provimux version when set, else the base Version.
func DisplayVersion() string {
	if ProvimuxVersionSet() {
		return strings.TrimSpace(ProvimuxVersion)
	}
	return Version
}
