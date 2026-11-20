package cliproxy

import "github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/savings"

// provimuxFlushSavings writes Savings' day totals while the local store is still open (Provimux
// layer; .github/provimux/design/savings/01-recording.md section 5). Called by shutdownKVStore, and by
// applyKVStoreConfig before a config reload may switch or close the store, so what was recorded
// before a change of server.kv-store goes to the store it was recorded under.
func provimuxFlushSavings() { savings.FlushNow() }
