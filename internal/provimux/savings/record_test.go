package savings

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// date-safe: the recorder's clock in these tests (sampleOf takes it as an argument).
var testNow = time.Date(2026, 11, 20, 11, 0, 0, 0, bucharest)

func sampleNow(rec coreusage.Record) sample {
	return sampleOf(context.Background(), rec, bucharest, testNow)
}

func TestSampleKind(t *testing.T) {
	for in, want := range map[string]string{"oauth": "oauth", "apikey": "apikey", " APIKey ": "apikey", "": "unknown", "token": "unknown"} {
		if got := sampleNow(coreusage.Record{AuthType: in}).kind; got != want {
			t.Errorf("kind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSampleHost(t *testing.T) {
	base := "https://user:secret-pass@OpenRouter.AI:443/api/v1?token=secret-token"
	if got := sampleNow(coreusage.Record{AuthType: "apikey", BaseURL: base}).host; got != "openrouter.ai" {
		t.Fatalf("host = %q", got)
	}
	if got := sampleNow(coreusage.Record{AuthType: "oauth", BaseURL: base}).host; got != "" {
		t.Fatalf("a login has no host, got %q", got)
	}
}

func TestSampleProviderAndModel(t *testing.T) {
	s := sampleNow(coreusage.Record{Provider: "  Claude ", Model: " Claude-Opus-5-5 "})
	if s.provider != "claude" || s.model != "claude-opus-5-5" {
		t.Fatalf("%q %q", s.provider, s.model)
	}
	s = sampleNow(coreusage.Record{Provider: strings.Repeat("p", 100), Model: strings.Repeat("é", 100)})
	if len(s.provider) != 64 || len(s.model) != 128 || !strings.HasPrefix(s.model, "é") {
		t.Fatalf("cut: %d %d", len(s.provider), len(s.model))
	}
	if s = sampleNow(coreusage.Record{}); s.provider != "unknown" || s.model != "unknown" {
		t.Fatalf("empty: %q %q", s.provider, s.model)
	}
}

func TestSampleTokensFromTheBreakdown(t *testing.T) {
	valid := coreusage.NewSubsetTokenBreakdown(1000, 600, 100, 50, 20, 1050)
	s := sampleNow(coreusage.Record{Provider: "codex", Detail: coreusage.Detail{InputTokens: 9, OutputTokens: 9, TokenBreakdown: valid}})
	if s.n.In != valid.Input.UncachedTokens || s.n.Cr != 600 || s.n.Cw != 100 || s.n.Out != 50 || s.n.Rsn != 20 {
		t.Fatalf("a valid breakdown is copied bucket by bucket: %+v (breakdown %+v)", s.n, valid)
	}
	s = sampleNow(coreusage.Record{Provider: "codex", Detail: coreusage.Detail{InputTokens: 1000, CacheReadTokens: 600, OutputTokens: 50}})
	if s.n.In != 400 || s.n.Cr != 600 || s.n.Out != 50 {
		t.Fatalf("an invalid breakdown is made by EnsureTokenBreakdownForProvider: %+v", s.n)
	}
	s = sampleNow(coreusage.Record{Provider: "acme", Detail: coreusage.Detail{InputTokens: 100, OutputTokens: 50}})
	if s.n.Unc != 150 || s.n.In != 0 || s.n.Out != 0 {
		t.Fatalf("an unknown provider's tokens are unc only: %+v", s.n)
	}
}

func TestSampleClaudeQuirk(t *testing.T) {
	s := sampleNow(coreusage.Record{Provider: "claude", Detail: coreusage.Detail{
		InputTokens: 10, CachedTokens: 5000, CacheReadTokens: 0, CacheCreationTokens: 5000, OutputTokens: 1}})
	if s.n.Cr != 0 || s.n.Cw != 5000 || s.n.In != 10 {
		t.Fatalf("CachedTokens must not be read as cache hits: %+v", s.n)
	}
}

func TestSampleLifetimeSplit(t *testing.T) {
	detail := coreusage.Detail{InputTokens: 10, CacheCreationTokens: 5000, OutputTokens: 1}
	if s := sampleNow(coreusage.Record{Provider: "claude", Detail: detail}); s.n.Cw5 != 0 || s.n.Cw1h != 0 {
		t.Fatalf("nil is 0: %+v", s.n)
	}
	detail.CacheCreation5mTokens, detail.CacheCreation1hTokens = coreusage.Int64Ptr(2000), coreusage.Int64Ptr(3000)
	if s := sampleNow(coreusage.Record{Provider: "claude", Detail: detail}); s.n.Cw5 != 2000 || s.n.Cw1h != 3000 {
		t.Fatalf("split: %+v", s.n)
	}
	detail.CacheCreation1hTokens = coreusage.Int64Ptr(3001)
	if s := sampleNow(coreusage.Record{Provider: "claude", Detail: detail}); s.n.Cw5 != 0 || s.n.Cw1h != 0 || s.n.Cw != 5000 {
		t.Fatalf("cw5 + cw1h > cw records both as 0: %+v", s.n)
	}
}

func TestSampleFailure(t *testing.T) {
	tokens := coreusage.Detail{InputTokens: 10, OutputTokens: 5}
	if s := sampleNow(coreusage.Record{Provider: "claude", Failed: true, Detail: tokens}); s.n.Fail != 1 || s.n.In != 10 {
		t.Fatalf("Failed, tokens counted: %+v", s.n)
	}
	for status, want := range map[int]int64{500: 1, 400: 1, 200: 0} {
		ctx := internallogging.WithResponseStatusHolder(context.Background())
		internallogging.SetResponseStatus(ctx, status)
		if s := sampleOf(ctx, coreusage.Record{}, bucharest, testNow); s.n.Fail != want {
			t.Errorf("status %d: fail %d", status, s.n.Fail)
		}
	}
}

func TestSampleCost(t *testing.T) {
	s := sampleNow(coreusage.Record{Detail: coreusage.Detail{CostDetails: &coreusage.CostDetails{Cost: coreusage.Float64Ptr(0.0123)}}})
	if s.n.Cost != 12_300_000 || s.n.Costn != 1 || s.n.Ucost != 0 {
		t.Fatalf("cost: %+v", s.n)
	}
	s = sampleNow(coreusage.Record{Detail: coreusage.Detail{CostDetails: &coreusage.CostDetails{Cost: coreusage.Float64Ptr(-1)}}})
	if s.n.Cost != 0 || s.n.Costn != 0 {
		t.Fatalf("a negative cost is ignored: %+v", s.n)
	}
	upstream := coreusage.Float64Ptr(0.5)
	s = sampleNow(coreusage.Record{Detail: coreusage.Detail{CostDetails: &coreusage.CostDetails{Cost: coreusage.Float64Ptr(0.01), UpstreamInferenceCost: upstream}}})
	if s.n.Ucost != 0 {
		t.Fatalf("ucost only with IsBYOK: %+v", s.n)
	}
	s = sampleNow(coreusage.Record{Detail: coreusage.Detail{CostDetails: &coreusage.CostDetails{Cost: coreusage.Float64Ptr(0.01), UpstreamInferenceCost: upstream, IsBYOK: coreusage.BoolPtr(true)}}})
	if s.n.Ucost != 500_000_000 || s.n.Cost != 10_000_000 {
		t.Fatalf("byok: %+v", s.n)
	}
}

func TestSampleDay(t *testing.T) {
	// date-safe: request times against testNow; sampleOf reads no clock.
	at := func(rt time.Time) string {
		return sampleOf(context.Background(), coreusage.Record{RequestedAt: rt}, bucharest, testNow).day
	}
	if a, b := at(time.Date(2026, 11, 19, 23, 59, 59, 0, bucharest)), at(time.Date(2026, 11, 20, 0, 0, 0, 0, bucharest)); a != "2026-11-19" || b != "2026-11-20" {
		t.Fatalf("%s %s", a, b)
	}
	// 2026-11-19 22:30 UTC is the 20th in Bucharest whatever the machine's zone.
	if got := at(time.Date(2026, 11, 19, 22, 30, 0, 0, time.UTC)); got != "2026-11-20" {
		t.Fatalf("the injected zone is used: %s", got)
	}
	// Daylight saving in Bucharest: 2026-03-29 (23 hours) and 2026-10-25 (25 hours).
	if got := at(time.Date(2026, 3, 29, 21, 59, 0, 0, time.UTC)); got != "2026-03-30" {
		t.Fatalf("spring: %s", got)
	}
	if got := at(time.Date(2026, 3, 28, 21, 59, 0, 0, time.UTC)); got != "2026-03-28" {
		t.Fatalf("before spring: %s", got)
	}
	if got := at(time.Date(2026, 10, 25, 21, 59, 0, 0, time.UTC)); got != "2026-10-25" {
		t.Fatalf("autumn, 23:59 local: %s", got)
	}
	if got := at(time.Time{}); got != "2026-11-20" {
		t.Fatalf("a zero RequestedAt uses the clock: %s", got)
	}
}

// Nothing else of a record is read: markers in every other field never reach the store. Alias, the
// model name the client sent, is kept since only-my-names round 2, apart from the rows (requested).
func TestNothingElseIsRead(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	headers := http.Header{}
	headers.Set("X-Marker", "marker-response-header")
	env.record(coreusage.Record{
		Provider: "claude", Model: "claude-opus-5-5", AuthType: "apikey",
		BaseURL: "https://user:marker-pass@api.anthropic.com/marker-path?marker-query=1",
		APIKey:  "marker-client-key-0123456789", Source: "marker-source@example.com", AuthID: "marker-auth-id",
		AuthIndex: "marker-auth-index", Alias: "marker-alias", SessionID: "marker-session",
		Fail: coreusage.Failure{Body: "marker-fail-body"}, ResponseHeaders: headers,
		Detail: coreusage.Detail{InputTokens: 1, OutputTokens: 1},
	})
	env.round()
	file := store.file(t)
	for _, marker := range []string{"marker-pass", "marker-path", "marker-query", "marker-client-key", "marker-source",
		"marker-auth-id", "marker-auth-index", "marker-session", "marker-fail-body", "marker-response-header"} {
		if bytes.Contains(file, []byte(marker)) {
			t.Errorf("%s reached the store's file", marker)
		}
	}
	if !bytes.Contains(file, []byte("api.anthropic.com")) {
		t.Fatal("premise: the day was written")
	}
	if !bytes.Contains(file, []byte(`"requested":[{"m":"marker-alias"`)) {
		t.Fatalf("the requested name is not kept apart from the rows: %s", file)
	}
}

// HandleUsage does no I/O: with a round stuck in a write, records are still taken.
func TestHandleUsageDoesNoIO(t *testing.T) {
	store := openTestStore(t, "")
	env := newTestEnv(t, store)
	env.record(claudeRecord("claude-opus-5-5", "k", 1, 0, 0, 1))
	store.mu.Lock()
	store.block, store.entered = make(chan struct{}), make(chan struct{}, 1)
	store.mu.Unlock()
	done := make(chan struct{})
	go func() {
		env.round()
		close(done)
	}()
	<-store.entered
	for i := 0; i < 1000; i++ {
		env.record(claudeRecord("claude-opus-5-5", "k", 1, 0, 0, 1))
	}
	store.mu.Lock()
	close(store.block)
	store.block = nil
	store.mu.Unlock()
	<-done
	env.round()
	// date-safe: the recorder runs on the test clock (newTestEnv); this is a stored or recorded day.
	if got := sumRows(storedDay(t, store, "2026-11-20")).Req; got != 1001 {
		t.Fatalf("req = %d, want 1001", got)
	}
}

func BenchmarkHandleUsage(b *testing.B) {
	env := newTestEnv(b, nil)
	rec := claudeRecord("claude-opus-5-5", "client-key-0123456789", 100, 1000, 10, 50)
	rec.RequestedAt = testNow
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		env.r.HandleUsage(ctx, rec)
	}
}
