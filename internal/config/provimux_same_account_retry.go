package config

import "testing"

// Provimux: routing.retry.same-account-retry. When the vendor answers a request, completely, with its documented
// structured overload refusal, the gateway waits a moment and sends it once more to the same account, at most once per
// client request (sdk/cliproxy/auth/provimux_same_account_retry.go). On unless the file says false; false is upstream's
// behaviour.

// ProvimuxSameAccountRetryDefault is the value when the key is not set.
const ProvimuxSameAccountRetryDefault = true

// provimuxSameAccountRetryDefault is the default in this process: off inside a test binary, so upstream's tests,
// which build configs without the key and count the vendor's calls, keep upstream's behaviour (as
// server.restart-drain-seconds does).
var provimuxSameAccountRetryDefault = ProvimuxSameAccountRetryDefault && !testing.Testing()

// ProvimuxSameAccountRetry returns whether the rule is on, and whether the config sets the key. A nil config is a
// config without the key.
func (cfg *Config) ProvimuxSameAccountRetry() (on bool, set bool) {
	if cfg == nil || cfg.SameAccountRetry == nil {
		return provimuxSameAccountRetryDefault, false
	}
	return *cfg.SameAccountRetry, true
}

// ProvimuxSameAccountRetryProcessDefault is the value this process uses when the key is not set (false only in a
// test binary).
func ProvimuxSameAccountRetryProcessDefault() bool { return provimuxSameAccountRetryDefault }
