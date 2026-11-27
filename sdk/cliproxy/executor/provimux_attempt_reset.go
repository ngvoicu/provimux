package executor

import "context"

// ProvimuxResetUpstreamAttempt clears what one try left on the attempt's tracker (that it reached its upstream, that
// plugin code produced or was handed its bytes, the model the provider chose), so that a second try on the same account
// in the same attempt (routing.retry.same-account-retry) is stated by what it does itself, not by what the first try did.
// Without a tracker on ctx it does nothing.
func ProvimuxResetUpstreamAttempt(ctx context.Context) {
	if ctx == nil {
		return
	}
	tracker, ok := ctx.Value(upstreamAttemptTrackerContextKey{}).(*upstreamAttemptTracker)
	if !ok || tracker == nil {
		return
	}
	tracker.attempted.Store(false)
	tracker.pluginBytes.Store(false)
	tracker.model.Store(nil)
}
