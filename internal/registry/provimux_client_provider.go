package registry

// Provimux layer: the account behind a usage record, for Savings (.github/provimux/design/savings/
// 01-recording.md section 2). An executor can serve another provider's account (Kimi's Claude Messages
// route runs the embedded Claude executor), so the executor's identifier is not the account's provider.

// ProvimuxClientProvider returns the provider a client (an auth ID) registered its models under, lower
// case as registered; "" when the client is unknown or registered none.
func ProvimuxClientProvider(clientID string) string {
	if clientID == "" {
		return ""
	}
	r := GetGlobalRegistry()
	r.mutex.RLock()
	defer r.mutex.RUnlock()
	return r.clientProviders[clientID]
}
