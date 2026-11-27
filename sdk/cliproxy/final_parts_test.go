package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/origin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sa "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	ca "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
)

// A connection-pool metrics decorator only has instrumentation for a net/http transport (finding 46). Ported (the
// amendment of 2026-11-27 (2), part 3): a program that installs its own decorator embeds the SDK; Provimux's own main
// did not start it, so no answer names an entry. The gateway as shipped installs no decorator but Provimux's own.
func TestFinalMetricsDecorator(t *testing.T) {
	for _, selective := range []bool{false, true} {
		t.Run(fmt.Sprint(selective), func(t *testing.T) {
			h := newSentHarness(t, reviewRouter)
			asEmbedding(t)
			tr, got := followTLS(t)
			origin.StandInForTests(tr)
			reviewSet(h, tr)
			var calls atomic.Int32
			metrics := func(next http.RoundTripper) http.RoundTripper {
				if _, supported := next.(*http.Transport); selective && !supported {
					return next
				}
				return reviewRT(func(r *http.Request) (*http.Response, error) { calls.Add(1); return next.RoundTrip(r) })
			}
			gateway := h.handler
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gateway.ServeHTTP(w, r.WithContext(httptransport.WithDecorator(r.Context(), metrics)))
			}))
			defer srv.Close()
			h.server = srv
			result := h.post("/v1/chat/completions", chatBody("review-public", false, ""), nil)
			receipts := followDrained(got)
			entry := result.get(logging.ProvimuxEntryHeader)
			t.Logf("public HTTP %d, custom transport calls=%d real vendor=%v entry=%q", result.status, calls.Load(), receipts, entry)
			if result.status != 200 || calls.Load() != 1 || len(receipts) != 1 || receipts[0].model != "lab/native" {
				t.Fatal("fixture did not traverse the embedding's transport")
			}
			if entry != "" {
				t.Error("an answer over an embedding's own transport named an entry")
			}
		})
	}
}

// Finding 45, with the gateway as shipped (the harness's process mark), and as an embedding: an executor registered
// through the SDK and executor registrations while the call runs (rule A.1, the registrations counted at the call's
// first attempt), and the process mark absent (part 3).
func TestFinalInFlightExecutor(t *testing.T) {
	for _, embedding := range []bool{false, true} {
		for _, replacements := range []int{0, 1, 2} {
			t.Run(fmt.Sprintf("embedding=%v/%d", embedding, replacements), func(t *testing.T) {
				finalInFlight(t, embedding, replacements)
			})
		}
	}
}

func finalInFlight(t *testing.T, embedding bool, replacements int) {
	h := newSentHarness(t, reviewRouter)
	if embedding {
		asEmbedding(t)
	}
	a := sentKeyFor(t, h, "review-public")
	m := h.service.coreManager
	e, _ := m.Executor(a.Provider)
	began, goOn := make(chan struct{}), make(chan struct{})
	m.RegisterExecutor(reviewExec{ProviderExecutor: e, run: func(c context.Context, a *ca.Auth, r ex.Request, o ex.Options) (ex.Response, error) {
		close(began)
		<-goOn
		return e.Execute(c, a, r, o)
	}})
	result := make(chan sentAnswer, 1)
	go func() { result <- h.post("/v1/chat/completions", chatBody("review-public", false, ""), nil) }()
	select {
	case <-began:
	case <-time.After(3 * time.Second):
		t.Fatal("custom executor did not start")
	}
	for i := 0; i < replacements; i++ {
		h.service.registerExecutorForAuth(a, true)
	}
	close(goOn)
	out := <-result
	receipts := h.stand.answered()
	entry := out.get(logging.ProvimuxEntryHeader)
	t.Logf("in-flight custom executor, service re-registrations=%d, HTTP=%d receipts=%v entry=%q", replacements, out.status, receipts, entry)
	if out.status != 200 || len(receipts) != 1 {
		t.Fatal("fixture")
	}
	if entry != "" {
		t.Error("a replacement was credited for the in-flight custom executor's answer")
	}
}

// Finding 47, with the gateway as shipped (the loaded mark holds for its id only, rule A.3) and as an embedding (the
// process mark absent, part 3).
func TestFinalCredentialCopy(t *testing.T) {
	for _, embedding := range []bool{false, true} {
		for _, copied := range []bool{false, true} {
			t.Run(fmt.Sprintf("embedding=%v/%v", embedding, copied), func(t *testing.T) {
				finalCredentialCopy(t, embedding, copied)
			})
		}
	}
}

func finalCredentialCopy(t *testing.T, embedding, copied bool) {
	h := newSentHarness(t, "")
	if embedding {
		asEmbedding(t)
	}
	store := sa.NewFileTokenStore()
	dir := t.TempDir()
	store.SetBaseDir(dir)
	if err := os.WriteFile(filepath.Join(dir, "source.json"), []byte(`{"type":"xai","access_token":"fixture-source","email":"source-fixture-account"}`), 0600); err != nil {
		t.Fatal(err)
	}
	list, err := store.List(t.Context())
	if err != nil || len(list) != 1 || !list[0].ProvimuxLoaded() {
		t.Fatal("source fixture", err)
	}
	a := &ca.Auth{}
	if copied {
		a = list[0].Clone()
	}
	a.ID = "sdk-ephemeral"
	a.FileName = ""
	a.Provider = "xai"
	a.Status = ca.StatusActive
	a.Storage = nil
	a.Metadata = nil
	a.Attributes = map[string]string{"auth_kind": "apikey", "api_key": "fixture-different-credential", "base_url": "https://api.x.ai/v1"}
	a, err = h.service.coreManager.Register(ca.WithSkipPersist(t.Context()), a)
	if err != nil {
		t.Fatal(err)
	}
	h.service.registerExecutorForAuth(a, false)
	id := "local-fixture-current"
	registry.GetGlobalRegistry().RegisterClient(a.ID, "xai", []*registry.ModelInfo{{ID: id}})
	defer registry.GetGlobalRegistry().UnregisterClient(a.ID)
	out := h.post("/v1/chat/completions", chatBody(id, false, ""), nil)
	entry := out.get(logging.ProvimuxEntryHeader)
	t.Logf("new in-memory ID and credentials, clone=%v loader mark=%v HTTP=%d entry=%q sent=%q receipts=%v", copied, a.ProvimuxLoaded(), out.status, entry, out.get(logging.ProvimuxModelSentHeader), h.stand.answered())
	if out.status != 200 || len(h.stand.answered()) != 1 {
		t.Fatal("fixture", out.body)
	}
	if entry != "" {
		t.Error("a different credential supplied only by the SDK was credited as loaded")
	}
}
