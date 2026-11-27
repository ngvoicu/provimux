package cliproxy

import (
	"context"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	ca "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	ex "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"testing"
	"time"
)

func TestFinalInFlightWatcherReload(t *testing.T) {
	for _, replacements := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(replacements), func(t *testing.T) {
			h := newSentHarness(t, reviewRouter)
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
				cfg := h.service.currentConfig().CloneForRuntime()
				enabled := true
				cfg.Models.AnsweredBy = &enabled
				h.service.applyWatcherConfigUpdate(cfg)
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
		})
	}
}
