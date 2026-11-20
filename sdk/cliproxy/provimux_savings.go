package cliproxy

import "github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/savings"

// provimuxFlushSavings writes what Savings recorded since its last save while the local store is
// still open (Provimux layer; .github/provimux/design/savings/01-recording.md section 5). Called by
// shutdownKVStore, and by applyKVStoreConfig before a config reload may switch or close the store, so
// what was recorded before a change of server.kv-store normally goes to the store it was recorded
// under. Not needed for correctness: what a flush cannot write stays in memory and goes to the store
// in use at the next round.
func provimuxFlushSavings() { savings.FlushNow() }
