package cliproxy

import "github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/savings"

// provimuxFlushSavings writes Savings' day totals while the local store is still open (Provimux
// layer; .github/provimux/design/savings/01-recording.md section 5). Called by shutdownKVStore.
func provimuxFlushSavings() { savings.FlushNow() }
