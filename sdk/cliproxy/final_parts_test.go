package cliproxy

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	ca "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

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
