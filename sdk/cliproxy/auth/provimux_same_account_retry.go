package auth

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
)

// Provimux: one second try on the same account when the vendor refused the request as overloaded
// (routing.retry.same-account-retry, on by default).
//
// A second try is sent only when the vendor itself answered, completely, with an error that says in its own structured
// fields that it is overloaded: then it refused the request without starting it, and sending it again cannot start
// anything twice. All of this is required:
//
//   - the try's HTTP exchanges were observed (the request-scoped transport seam, sdk/httptransport) and every one of them
//     got a complete response: status line, headers and the whole body, read to its end; none had a 2xx (or any
//     non-error) status, none failed without a response (refused, reset, closed, timed out, cut short: the vendor may
//     have accepted the request);
//   - the last of them went to the vendor's own API host and has a status its documentation names for the refusal
//     (provimuxOverloadAllowList), and no x-should-retry: false;
//   - its body, parsed as the account's vendor's error format, carries no spend, quota, billing or rate-limit code
//     (provimuxLimitCodes, checked first), and carries the vendor's documented overload value;
//   - the client call has not had its second try yet (one per client call, whatever the rounds, accounts, model pools,
//     fallbacks and refreshes around it), the request is not one that creates something when sent nor offers the image
//     tool, it is not a
//     WebSocket turn, the gateway is not in Home mode, and no request-scoped-errors rule of the owner matches.
//
// Upstream calls the executor in two places, each through provimuxSameAccountOnce (one changed line each):
// executeMixedOnce (conductor_execution.go) and executeStreamWithModelPool (conductor_stream.go). Upstream's code sees
// only the second try's result: the refused first try records no cooldown and moves no binding.

const (
	// provimuxRetryWait is the middle of the wait before the second try; provimuxRetryJitter the most it moves either
	// way: 500 to 1000 ms.
	provimuxRetryWait   = 750 * time.Millisecond
	provimuxRetryJitter = 250 * time.Millisecond
	// ProvimuxRetryAfterMax is the longest Retry-After of the vendor that still allows a second try. A Retry-After of 1
	// or 2 seconds is waited exactly; 0 (or none) means the jittered wait; more than 2 seconds means no second try.
	ProvimuxRetryAfterMax = 2 * time.Second
	// provimuxObservedBodyMax is the largest error body the rule reads; a larger one is never an overload refusal here.
	provimuxObservedBodyMax = 64 << 10
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

// provimuxOverloadAllowList is, per built-in executor (the account's provider), where and how its vendor's public
// documentation says "overloaded: the request was not processed; try again": the vendor's own API hosts (https, the
// default port), the statuses that carry it, and the structured value of the error body. It applies only there: an
// account of that executor with another base address, another status, or another body never gets a second try, and
// an executor without an entry never does.
var provimuxOverloadAllowList = map[string]provimuxVendorEntry{
	// Anthropic: "529 - overloaded_error: The API is temporarily overloaded" and "A 503 with overloaded_error counts
	// as a 5xx" (https://platform.claude.com/docs/en/api/errors); 502 is the status the owner's gateway received it
	// under. Its API is api.anthropic.com, the Claude executor's default base address.
	"claude": {
		hosts:    []string{"api.anthropic.com"},
		statuses: map[int]bool{529: true, http.StatusServiceUnavailable: true, http.StatusBadGateway: true},
		refusal: func(body []byte) bool {
			return ojson.GetBytesNoCopy(body, "type").String() == "error" &&
				ojson.GetBytesNoCopy(body, "error.type").String() == "overloaded_error"
		},
	},
	// OpenAI: "503 - Model temporarily overloaded. Type: service_unavailable_error, Code: server_is_overloaded. Cause:
	// The requested model is temporarily overloaded" (https://developers.openai.com/api/docs/guides/error-codes). Its
	// hosts: the API (api.openai.com) and the ChatGPT Codex backend the Codex executor uses by default (chatgpt.com).
	"codex": {
		hosts:    []string{"api.openai.com", "chatgpt.com"},
		statuses: map[int]bool{http.StatusServiceUnavailable: true},
		refusal: func(body []byte) bool {
			return ojson.GetBytesNoCopy(body, "error.code").String() == "server_is_overloaded" &&
				ojson.GetBytesNoCopy(body, "error.type").String() == "service_unavailable_error"
		},
	},
}

type provimuxVendorEntry struct {
	hosts    []string
	statuses map[int]bool
	refusal  func(body []byte) bool
}

// provimuxVendorHost reports whether a request went to one of the provider's own hosts: https, the default port. Tests,
// whose stand-ins listen on loopback, replace it (ProvimuxSetVendorHostsForTest).
var provimuxVendorHost = func(provider, scheme, host, port string) bool {
	entry, listed := provimuxOverloadAllowList[provider]
	if !listed || scheme != "https" || (port != "" && port != "443") {
		return false
	}
	for _, own := range entry.hosts {
		if strings.EqualFold(host, own) {
			return true
		}
	}
	return false
}

// ProvimuxSetVendorHostsForTest makes every host count as the provider's own, so that tests can put a loopback
// stand-in in the vendor's place, and returns the function that puts the real check back. For tests only.
func ProvimuxSetVendorHostsForTest() (restore func()) {
	previous := provimuxVendorHost
	provimuxVendorHost = func(string, string, string, string) bool { return true }
	return func() { provimuxVendorHost = previous }
}

// provimuxLimitCodes are the structured values that say a spend, quota, billing or rate limit. Checked before the
// allow-list, and they win wherever they appear in the body's code and type fields. Sources:
// OpenAI (https://developers.openai.com/api/docs/guides/error-codes): insufficient_quota, credit_balance_exhausted,
// organization_spend_limit_exceeded, project_spend_limit_exceeded, organization_usage_limit_exceeded, rate_limit_error,
// slow_down, rate_limit_exceeded; Anthropic (https://platform.claude.com/docs/en/api/errors): rate_limit_error,
// billing_error; Google (google.rpc.Code, https://github.com/googleapis/googleapis/blob/master/google/rpc/code.proto):
// RESOURCE_EXHAUSTED; the Codex backend as upstream's executor classifies it: usage_limit_reached,
// usage_not_included, turn_rate_limit_exceeded.
var provimuxLimitCodes = map[string]bool{
	"insufficient_quota": true, "credit_balance_exhausted": true, "organization_spend_limit_exceeded": true,
	"project_spend_limit_exceeded": true, "organization_usage_limit_exceeded": true, "rate_limit_error": true,
	"slow_down": true, "rate_limit_exceeded": true, "billing_error": true, "resource_exhausted": true,
	"usage_limit_reached": true, "usage_not_included": true, "turn_rate_limit_exceeded": true,
}

// The counts since the process started, for the state path. They count what the gateway itself does: whether a request
// reached the vendor's connection or an answer's first byte arrived is not what they say.
var (
	provimuxRetrySince        = time.Now()
	provimuxSecondCalls       atomic.Uint64
	provimuxSecondCallsOK     atomic.Uint64
	provimuxSecondCallsFailed atomic.Uint64
	provimuxLeftDuringWait    atomic.Uint64
)

// ProvimuxSameAccountRetryCounts is what the rule did since the process started.
type ProvimuxSameAccountRetryCounts struct {
	Since time.Time
	// SecondCalls: the executor was called a second time on the same account (after the wait).
	SecondCalls uint64
	// SecondCallsWithoutError: of those, the call returned without an error (for a stream: the stream was handed on).
	SecondCallsWithoutError uint64
	// SecondCallsWithError: of those, the call returned an error; the gateway went on as without the rule.
	SecondCallsWithError uint64
	// LeftDuringWait: refusals after which the client went away during the wait; the executor was not called again.
	LeftDuringWait uint64
}

// ProvimuxSameAccountRetryStats returns the counts since the process started. Each is read on its own: under load the
// four are not one snapshot.
func ProvimuxSameAccountRetryStats() ProvimuxSameAccountRetryCounts {
	return ProvimuxSameAccountRetryCounts{
		Since:                   provimuxRetrySince,
		SecondCalls:             provimuxSecondCalls.Load(),
		SecondCallsWithoutError: provimuxSecondCallsOK.Load(),
		SecondCallsWithError:    provimuxSecondCallsFailed.Load(),
		LeftDuringWait:          provimuxLeftDuringWait.Load(),
	}
}

// provimuxSameAccountOnce is upstream's executor call of one try, call(ctx, *auth, req, opts), and when the rule
// allows (see the top of this file) a second try on the same account after the wait. auth points at upstream's
// variable and sync is upstream's syncActiveAuth, run before the second try, as upstream reads the account after a
// call. The rule off, or not applying to this try, it is call(ctx, *auth, req, opts) and nothing else.
func provimuxSameAccountOnce[T any](m *Manager, ctx context.Context, auth **Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, sync func(), call func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (T, error)) (T, error) {
	if !m.provimuxRuleApplies(ctx, *auth, opts) {
		return call(ctx, *auth, req, opts)
	}
	observed := &provimuxObservation{}
	first, errFirst := call(provimuxObserve(ctx, observed), *auth, req, opts)
	if errFirst == nil {
		return first, nil
	}
	wait, refusal, ok := m.provimuxSecondTryAllowed(ctx, *auth, errFirst, observed)
	if !ok || !provimuxTakeSecondTry(ctx) {
		return first, errFirst
	}
	if result, isStream := any(first).(*cliproxyexecutor.StreamResult); isStream && result != nil {
		discardStreamChunks(result.Chunks)
	}
	entry := logEntryWithRequestID(ctx)
	account := formatAuthIdentity(*auth, provimuxAuthProvider(*auth))
	if errWait := provimuxRetrySleep(ctx, wait); errWait != nil {
		provimuxLeftDuringWait.Add(1)
		entry.Infof("same-account retry: %s on %s; the client left during the wait, the executor was not called again", refusal, account)
		var zero T
		return zero, errFirst
	}
	// The second try is stated by what it does itself: nothing the first try left on the attempt's tracker or in its
	// recorded response headers carries over.
	cliproxyexecutor.ProvimuxResetUpstreamAttempt(ctx)
	internallogging.SetResponseHeaders(ctx, nil)
	if sync != nil {
		sync()
	}
	provimuxSecondCalls.Add(1)
	second, errSecond := call(ctx, *auth, req, opts)
	if errSecond == nil {
		provimuxSecondCallsOK.Add(1)
		entry.Infof("same-account retry: %s on %s; called the executor a second time after %v: it returned without an error", refusal, account, wait.Round(time.Millisecond))
		return second, nil
	}
	provimuxSecondCallsFailed.Add(1)
	entry.Infof("same-account retry: %s on %s; called the executor a second time after %v: it returned an error (%s), going on as without the rule", refusal, account, wait.Round(time.Millisecond), provimuxStatusText(errSecond))
	return second, errSecond
}

// provimuxRuleApplies is what can be known before the first try: the rule is on, the gateway is not in Home mode, the
// account's executor has an entry in the allow-list, the client call still has its second try, the request may be
// sent twice and is not a WebSocket turn.
func (m *Manager) provimuxRuleApplies(ctx context.Context, auth *Auth, opts cliproxyexecutor.Options) bool {
	if m == nil || auth == nil || ctx == nil {
		return false
	}
	if on, _ := m.runtimeConfigSnapshot().ProvimuxSameAccountRetry(); !on || m.HomeEnabled() {
		return false
	}
	if _, listed := provimuxOverloadAllowList[provimuxAuthProvider(auth)]; !listed {
		return false
	}
	if cliproxyexecutor.DownstreamWebsocket(ctx) || cliproxyexecutor.RequiredUpstreamWebsocket(ctx) {
		return false
	}
	return provimuxRepeatable(opts) && provimuxSecondTryAvailable(ctx)
}

// provimuxSecondTryAllowed decides, after a failed first try, whether the vendor refused it as overloaded, and the wait.
func (m *Manager) provimuxSecondTryAllowed(ctx context.Context, auth *Auth, err error, observed *provimuxObservation) (time.Duration, string, bool) {
	if ctx.Err() != nil || cliproxyexecutor.IsUpstreamWebsocketTurnError(err) {
		return 0, "", false
	}
	// A request-scoped-errors rule the owner wrote for this failure decides it, as without the rule.
	if _, matched := matchRequestScopedErrorAction(auth, err, m.requestScopedErrorConfig(ctx)); matched {
		return 0, "", false
	}
	response, ok := observed.refusal()
	if !ok {
		return 0, "", false
	}
	provider := provimuxAuthProvider(auth)
	if !provimuxVendorHost(provider, response.scheme, response.host, response.port) {
		return 0, "", false // the source of the allow-list entry is about the vendor's own API only
	}
	if strings.EqualFold(strings.TrimSpace(response.shouldRetry), "false") {
		return 0, "", false // the vendor says not to retry (x-should-retry: false)
	}
	if !ProvimuxOverloadRefusal(provider, response.status, response.body) {
		return 0, "", false
	}
	wait := provimuxRetryWait + provimuxRetryJitterOf()
	if strings.TrimSpace(response.retryAfter) != "" {
		retryAfter, valid := provimuxParseRetryAfter(response.retryAfter, time.Now())
		if !valid || retryAfter > ProvimuxRetryAfterMax {
			return 0, "", false // the vendor says it will be longer, or says something this cannot read
		}
		if retryAfter > 0 {
			wait = retryAfter
		}
	}
	return wait, "status " + strconv.Itoa(response.status) + " overloaded refusal", true
}

// ProvimuxOverloadRefusal is the table: whether a complete error response of status with the (decoded) body, received by
// an account of provider from the vendor's own host, is the vendor's documented overload refusal. A body with a limit
// code never is; otherwise the provider's allow-list entry decides (its statuses and its value), and a provider without
// one never is.
func ProvimuxOverloadRefusal(provider string, status int, body []byte) bool {
	entry, listed := provimuxOverloadAllowList[strings.ToLower(strings.TrimSpace(provider))]
	if !listed || !entry.statuses[status] {
		return false
	}
	if !ojson.ValidBytes(body) || provimuxCarriesLimitCode(body) {
		return false
	}
	return entry.refusal(body)
}

// provimuxCarriesLimitCode reports whether a code or type field of the body is a spend, quota, billing or rate-limit
// value: the top level and error object's type, code and status, and the reasons of error.details.
func provimuxCarriesLimitCode(body []byte) bool {
	for _, path := range []string{"type", "code", "status", "error.type", "error.code", "error.status"} {
		if provimuxLimitCodes[strings.ToLower(strings.TrimSpace(ojson.GetBytesNoCopy(body, path).String()))] {
			return true
		}
	}
	for _, detail := range ojson.GetBytesNoCopy(body, "error.details").Array() {
		if provimuxLimitCodes[strings.ToLower(strings.TrimSpace(detail.Get("reason").String()))] {
			return true
		}
	}
	return false
}

// provimuxRepeatable reports whether the request may be sent twice at all: not a job whose sending creates something
// (an image, a video, speech, a Gemini interaction), nor a client request that offers the image tool at all, however
// tool_choice is spelt. Only overload refusals are ever sent twice now, so this is a second guard.
func provimuxRepeatable(opts cliproxyexecutor.Options) bool {
	switch strings.ToLower(strings.TrimSpace(opts.SourceFormat.String())) {
	case "openai-image", "openai-video", "openai-speech", "interactions":
		return false
	}
	if len(opts.OriginalRequest) > 0 {
		if ojson.GetBytesNoCopy(opts.OriginalRequest, "tool_choice.type").String() == "image_generation" {
			return false
		}
		for _, tool := range ojson.GetBytesNoCopy(opts.OriginalRequest, "tools").Array() {
			if tool.Get("type").String() == "image_generation" {
				return false
			}
		}
	}
	return true
}

// One second try per client call. A client HTTP request carries its gin context ("gin" on the context, set by the
// handlers for the whole request, upstream's own retries of it included); a call through the SDK without one uses the
// call's own request-scoped map that Manager.Execute, ExecuteStream and ExecuteCount put on the context. Without either
// there is no second try.
const provimuxSecondTryKey = "\x00provimux:same-account-retry"

type provimuxKeyStore interface {
	Get(key string) (any, bool)
	Set(key string, value any)
}

var provimuxSecondTryMu sync.Mutex

func provimuxSecondTryAvailable(ctx context.Context) bool {
	return provimuxSecondTry(ctx, false)
}

func provimuxTakeSecondTry(ctx context.Context) bool {
	return provimuxSecondTry(ctx, true)
}

func provimuxSecondTry(ctx context.Context, take bool) bool {
	provimuxSecondTryMu.Lock()
	defer provimuxSecondTryMu.Unlock()
	if store, ok := ctx.Value("gin").(provimuxKeyStore); ok && store != nil {
		if _, used := store.Get(provimuxSecondTryKey); used {
			return false
		}
		if take {
			store.Set(provimuxSecondTryKey, true)
		}
		return true
	}
	if calls := requestRefusedAuthsFromContext(ctx); calls != nil {
		if _, used := calls[provimuxSecondTryKey]; used {
			return false
		}
		if take {
			calls[provimuxSecondTryKey] = struct{}{} // no account has this id: it never excludes one
		}
		return true
	}
	return false
}

// provimuxObservation is what one try's HTTP exchanges brought back, as the transport saw them.
type provimuxObservation struct {
	mu        sync.Mutex
	exchanges []*provimuxExchange
}

type provimuxExchange struct {
	mu          sync.Mutex
	scheme      string
	host        string
	port        string
	shouldRetry string
	failed      bool // no response arrived
	status      int
	length      int64 // Content-Length; -1 unknown
	encoding    string
	retryAfter  string
	body        bytes.Buffer
	read        int64
	eof         bool
	tooLarge    bool
}

type provimuxResponse struct {
	status             int
	body               []byte
	retryAfter         string
	shouldRetry        string
	scheme, host, port string
}

// refusal returns the try's last response when every exchange of the try got a complete error response (nothing
// failed without one, nothing had a non-error status, every body was read to its end), with its body decoded.
func (o *provimuxObservation) refusal() (provimuxResponse, bool) {
	o.mu.Lock()
	exchanges := append([]*provimuxExchange(nil), o.exchanges...)
	o.mu.Unlock()
	if len(exchanges) == 0 {
		return provimuxResponse{}, false
	}
	for _, exchange := range exchanges {
		exchange.mu.Lock()
		complete := !exchange.failed && exchange.status >= 400 && !exchange.tooLarge &&
			(exchange.eof || (exchange.length >= 0 && exchange.read == exchange.length))
		exchange.mu.Unlock()
		if !complete {
			return provimuxResponse{}, false
		}
	}
	last := exchanges[len(exchanges)-1]
	last.mu.Lock()
	defer last.mu.Unlock()
	body, ok := provimuxDecode(last.body.Bytes(), last.encoding)
	if !ok {
		return provimuxResponse{}, false
	}
	return provimuxResponse{status: last.status, body: body, retryAfter: last.retryAfter, shouldRetry: last.shouldRetry,
		scheme: last.scheme, host: last.host, port: last.port}, true
}

// provimuxObserve returns ctx with a transport decorator that notes each HTTP exchange of the try on observed. It
// changes no byte: a response with a non-error status is handed back as it came, an error response's body is read
// through a tap that keeps a copy of at most provimuxObservedBodyMax bytes. A decorator ctx already carried still runs,
// inside this one.
func provimuxObserve(ctx context.Context, observed *provimuxObservation) context.Context {
	outer := ctx
	return httptransport.WithDecorator(ctx, func(next http.RoundTripper) http.RoundTripper {
		inner := next
		if decorated := httptransport.Decorate(outer, &http.Client{Transport: next}); decorated != nil && decorated.Transport != nil {
			inner = decorated.Transport
		}
		return &provimuxObserver{next: inner, observed: observed}
	})
}

type provimuxObserver struct {
	next     http.RoundTripper
	observed *provimuxObservation
}

func (o *provimuxObserver) RoundTrip(req *http.Request) (*http.Response, error) {
	exchange := &provimuxExchange{length: -1}
	if req.URL != nil {
		exchange.scheme, exchange.host, exchange.port = strings.ToLower(req.URL.Scheme), req.URL.Hostname(), req.URL.Port()
	}
	o.observed.mu.Lock()
	o.observed.exchanges = append(o.observed.exchanges, exchange)
	o.observed.mu.Unlock()
	resp, err := o.next.RoundTrip(req)
	exchange.mu.Lock()
	defer exchange.mu.Unlock()
	if err != nil || resp == nil {
		exchange.failed = true
		return resp, err
	}
	exchange.status = resp.StatusCode
	if resp.StatusCode < 400 || resp.Body == nil {
		return resp, nil
	}
	exchange.length = resp.ContentLength
	if !resp.Uncompressed {
		exchange.encoding = resp.Header.Get("Content-Encoding")
	}
	exchange.retryAfter = resp.Header.Get("Retry-After")
	exchange.shouldRetry = resp.Header.Get("X-Should-Retry")
	resp.Body = &provimuxBodyTap{ReadCloser: resp.Body, exchange: exchange}
	return resp, nil
}

// provimuxBodyTap hands an error body on unchanged and keeps a copy of it.
type provimuxBodyTap struct {
	io.ReadCloser
	exchange *provimuxExchange
}

func (t *provimuxBodyTap) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	t.exchange.mu.Lock()
	t.exchange.read += int64(n)
	if t.exchange.body.Len()+n <= provimuxObservedBodyMax {
		t.exchange.body.Write(p[:n])
	} else {
		t.exchange.tooLarge = true
	}
	if err == io.EOF {
		t.exchange.eof = true
	}
	t.exchange.mu.Unlock()
	return n, err
}

// provimuxDecode undoes the Content-Encoding of a kept error body (gzip, deflate, br, zstd, in the header's order), or
// a gzip or zstd body sent without the header (as upstream's executors detect it). The result is at most four times
// provimuxObservedBodyMax.
func provimuxDecode(body []byte, encoding string) ([]byte, bool) {
	encodings := strings.Split(strings.ToLower(encoding), ",")
	if strings.TrimSpace(encoding) == "" {
		switch {
		case bytes.HasPrefix(body, []byte{0x1f, 0x8b}):
			encodings = []string{"gzip"}
		case bytes.HasPrefix(body, []byte{0x28, 0xb5, 0x2f, 0xfd}):
			encodings = []string{"zstd"}
		default:
			return body, true
		}
	}
	for i := len(encodings) - 1; i >= 0; i-- {
		var reader io.Reader
		var errReader error
		source := bytes.NewReader(body)
		switch strings.TrimSpace(encodings[i]) {
		case "", "identity":
			continue
		case "gzip", "x-gzip":
			reader, errReader = gzip.NewReader(source)
		case "deflate":
			var zr io.ReadCloser
			if zr, errReader = zlib.NewReader(source); errReader != nil {
				reader, errReader = flate.NewReader(bytes.NewReader(body)), nil
			} else {
				reader = zr
			}
		case "br":
			reader = brotli.NewReader(source)
		case "zstd":
			decoder, errZstd := zstd.NewReader(source)
			if errZstd != nil {
				return nil, false
			}
			defer decoder.Close()
			reader = decoder
		default:
			return nil, false
		}
		if errReader != nil {
			return nil, false
		}
		decoded, errRead := io.ReadAll(io.LimitReader(reader, 4*provimuxObservedBodyMax+1))
		if errRead != nil || len(decoded) > 4*provimuxObservedBodyMax {
			return nil, false
		}
		body = decoded
	}
	return body, true
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
	return strings.ToLower(strings.TrimSpace(auth.Provider))
}

// provimuxParseRetryAfter parses a Retry-After value (RFC 9110 10.2.3): a whole number of seconds, or an HTTP date
// (relative to now; a past date is 0).
func provimuxParseRetryAfter(raw string, now time.Time) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseUint(raw, 10, 32); err == nil {
		return time.Duration(seconds) * time.Second, true
	}
	if at, err := http.ParseTime(raw); err == nil {
		return max(at.Sub(now), 0), true
	}
	return 0, false
}
