package auth

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/ojson"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Provimux: once more on the same account after a passing fault (routing.retry.same-account-retry, on by default).
//
// When an attempt fails with a fault of the vendor that passes in a moment (it is overloaded or temporarily
// unavailable, or the connection failed before any answer byte) and nothing of the answer has been handed on yet, the
// same request goes once more to the same account after a short wait. Upstream's code sees only the second result: the
// first failure records no cooldown and moves no binding. If the second fails too, upstream goes on as it does today.
//
// Upstream calls the executor in two places, each through provimuxSameAccountOnce (one changed line each):
// executeMixedOnce (conductor_execution.go) and executeStreamWithModelPool (conductor_stream.go). A stream is handed
// over by ExecuteStream only after its status was read, so a fault returned there was sent before any answer byte; a
// fault inside a stream that has begun never reaches this function.

const (
	// provimuxRetryWait is the middle of the wait before the second try; provimuxRetryJitter the most it moves either
	// way: 500 to 1000 ms.
	provimuxRetryWait   = 750 * time.Millisecond
	provimuxRetryJitter = 250 * time.Millisecond
	// ProvimuxRetryAfterMax is the longest Retry-After the vendor may give for the fault to still count as passing.
	// The wait is then max(the vendor's Retry-After, the jittered wait), so a client waits at most this long more.
	ProvimuxRetryAfterMax = 2 * time.Second
)

// ProvimuxRetryWaitBounds returns the shortest and the longest wait without a Retry-After, for the state path.
func ProvimuxRetryWaitBounds() (time.Duration, time.Duration) {
	return provimuxRetryWait - provimuxRetryJitter, provimuxRetryWait + provimuxRetryJitter
}

// The seams the tests set: the jitter and the wait (a clock the test controls; nothing waits on the machine's speed).
var (
	provimuxRetryJitterOf = func() time.Duration {
		return time.Duration(rand.Int64N(int64(2*provimuxRetryJitter)+1)) - provimuxRetryJitter
	}
	provimuxRetrySleep = func(ctx context.Context, wait time.Duration) error {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
)

// ProvimuxSetRetryClockForTest replaces the wait and the jitter (nil keeps each) and returns the function that puts
// them back. For tests only: a test controls the wait and never waits on the machine's speed.
func ProvimuxSetRetryClockForTest(sleep func(context.Context, time.Duration) error, jitter func() time.Duration) (restore func()) {
	prevSleep, prevJitter := provimuxRetrySleep, provimuxRetryJitterOf
	if sleep != nil {
		provimuxRetrySleep = sleep
	}
	if jitter != nil {
		provimuxRetryJitterOf = jitter
	}
	return func() { provimuxRetrySleep, provimuxRetryJitterOf = prevSleep, prevJitter }
}

// The fault kinds, as the log lines and the tests name them.
const (
	provimuxFaultOverloaded  = "overloaded"  // the vendor says so in its body, or 529
	provimuxFaultUnavailable = "unavailable" // 502, 503, 504 with a body that says nothing else
	provimuxFaultConnection  = "connection"  // refused, reset or closed before any answer byte
)

// The counts since the process started, for the state path.
var (
	provimuxRetrySince   = time.Now()
	provimuxRetriesMade  atomic.Uint64
	provimuxRetriesSaved atomic.Uint64
	provimuxRetriesLost  atomic.Uint64
)

// ProvimuxSameAccountRetryCounts is what the rule did since the process started.
type ProvimuxSameAccountRetryCounts struct {
	Since    time.Time
	Retries  uint64 // second tries started
	Saved    uint64 // of those, answered
	NotSaved uint64 // of those, failed again (or the client went away during the wait); upstream went on as today
}

// ProvimuxSameAccountRetryStats returns the counts since the process started.
func ProvimuxSameAccountRetryStats() ProvimuxSameAccountRetryCounts {
	return ProvimuxSameAccountRetryCounts{
		Since:    provimuxRetrySince,
		Retries:  provimuxRetriesMade.Load(),
		Saved:    provimuxRetriesSaved.Load(),
		NotSaved: provimuxRetriesLost.Load(),
	}
}

// provimuxSameAccountOnce makes call, upstream's executor call of one attempt, and when it fails with a passing fault
// and the rule applies, waits and makes it once more on the same account, in the same attempt context. sync, when
// not nil, is upstream's syncActiveAuth, run before the second call; discard, when not nil, releases what the failed
// first call returned (a stream's channel). The rule off, it is call() and nothing
// else.
func provimuxSameAccountOnce[T any](m *Manager, ctx context.Context, auth *Auth, opts cliproxyexecutor.Options, sync func(), call func() (T, error), discard func(T)) (T, error) {
	first, errFirst := call()
	if errFirst == nil {
		return first, nil
	}
	kind, wait, ok := m.provimuxRetryable(ctx, auth, opts, errFirst)
	if !ok {
		return first, errFirst
	}
	if discard != nil {
		discard(first)
	}
	provimuxRetriesMade.Add(1)
	entry := logEntryWithRequestID(ctx)
	account := formatAuthIdentity(auth, provimuxAuthProvider(auth))
	status := provimuxStatusText(errFirst)
	if errWait := provimuxRetrySleep(ctx, wait); errWait != nil {
		provimuxRetriesLost.Add(1)
		entry.Infof("same-account retry: not saved: %s fault (%s) on %s; the client went away during the wait", kind, status, account)
		var zero T
		return zero, errFirst
	}
	if sync != nil {
		sync() // the account as the first try may have refreshed it, as upstream reads it after a call
	}
	second, errSecond := call()
	if errSecond == nil {
		provimuxRetriesSaved.Add(1)
		entry.Infof("same-account retry: saved: %s fault (%s) on %s; it answered the second time, after %v", kind, status, account, wait.Round(time.Millisecond))
		return second, nil
	}
	provimuxRetriesLost.Add(1)
	entry.Infof("same-account retry: not saved: %s fault (%s) on %s; the second try failed too (%s), going on as without the rule", kind, status, account, provimuxStatusText(errSecond))
	return second, errSecond
}

// provimuxStream keeps upstream's stream call short enough for gofmt to leave it on one line.
type provimuxStream = *cliproxyexecutor.StreamResult

// provimuxDiscardStream releases the stream a failed ExecuteStream returned, as upstream's abandonAttempt does.
func provimuxDiscardStream(result *cliproxyexecutor.StreamResult) {
	if result != nil {
		discardStreamChunks(result.Chunks)
	}
}

// provimuxStatusText names a failure for the log: its status, or that no answer came.
func provimuxStatusText(err error) string {
	if status := statusCodeFromError(err); status != 0 {
		return "status " + strconv.Itoa(status)
	}
	return "no answer"
}

func provimuxAuthProvider(auth *Auth) string {
	if auth == nil {
		return ""
	}
	return auth.Provider
}

// provimuxRetryable decides whether err, the first failure of an attempt of auth, is retried on the same account: the
// fault's kind and the wait when it is.
func (m *Manager) provimuxRetryable(ctx context.Context, auth *Auth, opts cliproxyexecutor.Options, err error) (string, time.Duration, bool) {
	if m == nil || auth == nil || ctx == nil || ctx.Err() != nil {
		return "", 0, false
	}
	cfg := m.runtimeConfigSnapshot()
	if on, _ := cfg.ProvimuxSameAccountRetry(); !on || m.HomeEnabled() {
		return "", 0, false
	}
	if !provimuxRepeatable(opts) {
		return "", 0, false
	}
	// A request-scoped-errors rule the owner wrote for this failure decides it, as without the rule.
	if _, matched := matchRequestScopedErrorAction(auth, err, m.requestScopedErrorConfig(ctx)); matched {
		return "", 0, false
	}
	kind, ok := ProvimuxPassingFault(err)
	if !ok {
		return "", 0, false
	}
	wait := provimuxRetryWait + provimuxRetryJitterOf()
	if retryAfter, given := provimuxVendorRetryAfter(ctx, err); given {
		if retryAfter > ProvimuxRetryAfterMax {
			return "", 0, false // the vendor says it will be longer: not passing
		}
		wait = max(wait, retryAfter)
	}
	return kind, wait, true
}

// provimuxRepeatable reports whether the request may be sent twice: not a job whose sending creates something
// (an image, a video, speech, a Gemini interaction, which can run in the background), nor a request that forces the
// image tool (the images endpoints routed through Responses).
func provimuxRepeatable(opts cliproxyexecutor.Options) bool {
	switch strings.ToLower(strings.TrimSpace(opts.SourceFormat.String())) {
	case "openai-image", "openai-video", "openai-speech", "interactions":
		return false
	}
	if len(opts.OriginalRequest) > 0 && ojson.GetBytesNoCopy(opts.OriginalRequest, "tool_choice.type").String() == "image_generation" {
		return false
	}
	return true
}

// ProvimuxPassingFault is the table of passing faults: the fault's kind and true when err is one. In order:
//
//	the client went away (context canceled or past its deadline)                        never
//	upstream marks it request-scoped, a truncated accepted stream, a lost upstream
//	WebSocket turn (never replayed), or a whole-credential quota                        never
//	the body states a quota, a rate or usage limit, a credit or billing problem         never
//	the body says the vendor is overloaded (overloaded_error, server_is_overloaded,
//	"overloaded" in its text), whatever the status                                      passing: overloaded
//	529                                                                                 passing: overloaded
//	502, 503, 504                                                                       passing: unavailable
//	no status: refused, reset, closed, timed out before any answer byte                 passing: connection
//	anything else (400, 401, 402, 403, 404, 408, 429, 500, ...)                         never
//
// The tests beside it: TestProvimuxPassingFaultTable (sdk/cliproxy/auth) and test/provimux/transient_retry_test.go.
func ProvimuxPassingFault(err error) (string, bool) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", false
	}
	if isRequestScopedError(err) || isStreamTruncatedError(err) || cliproxyexecutor.IsUpstreamWebsocketTurnError(err) || isCredentialScopedError(err) {
		return "", false
	}
	status := statusCodeFromError(err)
	text := strings.ToLower(err.Error())
	if provimuxStatesLimit(text) {
		return "", false
	}
	if strings.Contains(text, "overload") {
		return provimuxFaultOverloaded, true
	}
	switch status {
	case 529:
		return provimuxFaultOverloaded, true
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return provimuxFaultUnavailable, true
	case 0:
		if isTransientTransportError(err) {
			return provimuxFaultConnection, true
		}
	}
	return "", false
}

// provimuxLimitWords are what a body says when the fault is the account's quota or limit, never a passing one.
var provimuxLimitWords = []string{"quota", "rate limit", "rate_limit", "ratelimit", "usage limit", "usage_limit", "credit", "billing", "insufficient", "too many requests"}

func provimuxStatesLimit(text string) bool {
	for _, word := range provimuxLimitWords {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

// provimuxVendorRetryAfter reads the vendor's own Retry-After of the failed try: the response headers the executor
// recorded for the attempt, or those the error carries. It is the header as sent (an executor's cooldown hint may add a
// random margin to it).
func provimuxVendorRetryAfter(ctx context.Context, err error) (time.Duration, bool) {
	raw := ""
	if headers := internallogging.GetResponseHeaders(ctx); headers != nil {
		raw = headers.Get("Retry-After")
	}
	if raw == "" {
		var carrier interface{ Headers() http.Header }
		if errors.As(err, &carrier) && carrier != nil {
			if headers := carrier.Headers(); headers != nil {
				raw = headers.Get("Retry-After")
			}
		}
	}
	return provimuxParseRetryAfter(raw, time.Now())
}

// provimuxParseRetryAfter parses a Retry-After value: seconds, or an HTTP date relative to now.
func provimuxParseRetryAfter(raw string, now time.Time) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseFloat(raw, 64); err == nil {
		if seconds < 0 {
			return 0, true
		}
		return time.Duration(seconds * float64(time.Second)), true
	}
	if at, err := http.ParseTime(raw); err == nil {
		return max(at.Sub(now), 0), true
	}
	return 0, false
}
