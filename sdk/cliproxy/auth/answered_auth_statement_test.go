package auth

import (
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// The conductor's publication and the header commit race: the publisher switches between a hosted key, a login and
// no statement while the handler commits its headers (as a keep-alive would). Every response carries one whole
// statement or none: never a kind without its provider, or a provider without its host.
func TestAnsweredAuthStatementIsCommittedWholeOrNotAtAll(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hostedKey := &Auth{Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "made-up-key", "base_url": "https://openrouter.example.test/api/v1"}}
	login := &Auth{Provider: "devin", Attributes: map[string]string{AttributeAuthKind: "oauth"}}
	allowed := map[[3]string]bool{
		{"", "", ""}: true,
		{"apikey", "claude", "openrouter.example.test"}: true,
		{"oauth", "devin", ""}:                          true,
	}

	iteration := 0
	engine := gin.New()
	engine.Use(logging.CPATraceIDMiddleware())
	engine.GET("/", func(c *gin.Context) {
		meta := map[string]any{cliproxyexecutor.AnsweredAuthCallbackMetadataKey: logging.GinCPAAuthStatementCallback(c)}
		mode := iteration % 4
		started := make(chan struct{})
		done := make(chan struct{})
		go func() {
			close(started)
			if mode == 2 {
				runtime.Gosched()
			}
			for i := 0; i < 8; i++ {
				switch i % 3 {
				case 0:
					publishAnsweredAuthMetadata(meta, hostedKey)
				case 1:
					publishAnsweredAuthMetadata(meta, login)
				default:
					publishSelectedAuthMetadata(meta, hostedKey)
				}
			}
			close(done)
		}()
		<-started
		if mode == 1 || mode == 3 {
			runtime.Gosched()
		}
		_, _ = c.Writer.Write([]byte("\n"))
		c.Writer.Flush()
		<-done
	})

	seen := map[[3]string]int{}
	for iteration = 0; iteration < 20000; iteration++ {
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))
		header := recorder.Result().Header
		got := [3]string{header.Get(logging.CPAAuthKindHeader), header.Get(logging.CPAAuthProviderHeader), header.Get(logging.CPAAuthHostHeader)}
		if !allowed[got] {
			t.Fatalf("response %d carries part of a statement: kind=%q provider=%q host=%q", iteration, got[0], got[1], got[2])
		}
		seen[got]++
	}
	for statement, count := range seen {
		t.Logf("kind=%q provider=%q host=%q: %d", statement[0], statement[1], statement[2], count)
	}
	if len(seen) < 2 {
		t.Fatalf("the publisher and the commit never overlapped: %v", seen)
	}
}
