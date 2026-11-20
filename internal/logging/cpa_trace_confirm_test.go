package logging

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// A response names a credential only with a statement a route confirmed: every way the headers can leave the writer,
// from every state of the request, carries the confirmed statement whole, or none of the three headers. The header
// map starts with foreign values for the three names (a staged status, a writer that put them there): a commit
// without a confirmed statement removes them, a commit with one replaces them.
func TestOnlyAConfirmedStatementIsCommitted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	answered := AuthStatement{Kind: "apikey", Provider: "claude", Host: "openrouter.example.test"}
	other := AuthStatement{Kind: "oauth", Provider: "devin"}

	states := []struct {
		name      string
		set       func(*gin.Context)
		confirmed bool
	}{
		{name: "nothing", set: func(*gin.Context) {}},
		{name: "offered only", set: func(c *gin.Context) { OfferGinCPAAuthStatement(c, answered) }},
		{name: "offered then confirmed", set: func(c *gin.Context) {
			OfferGinCPAAuthStatement(c, answered)
			ConfirmGinCPAAuthOffer(c)
		}, confirmed: true},
		{name: "confirmed directly", set: func(c *gin.Context) { ConfirmGinCPAAuthStatement(c, answered) }, confirmed: true},
		{name: "confirmed then a new offer", set: func(c *gin.Context) {
			ConfirmGinCPAAuthStatement(c, other)
			OfferGinCPAAuthStatement(c, answered)
		}},
	}
	entries := []struct {
		name   string
		commit func(*gin.Context)
	}{
		{name: "WriteHeader then end of handler", commit: func(c *gin.Context) { c.Writer.WriteHeader(http.StatusAccepted) }},
		{name: "WriteHeaderNow", commit: func(c *gin.Context) {
			c.Writer.WriteHeader(http.StatusAccepted)
			c.Writer.WriteHeaderNow()
		}},
		{name: "Write", commit: func(c *gin.Context) {
			c.Writer.WriteHeader(http.StatusAccepted)
			_, _ = c.Writer.Write([]byte("answer"))
		}},
		{name: "WriteString", commit: func(c *gin.Context) {
			c.Writer.WriteHeader(http.StatusAccepted)
			_, _ = c.Writer.WriteString("answer")
		}},
		{name: "Flush", commit: func(c *gin.Context) {
			c.Writer.WriteHeader(http.StatusAccepted)
			c.Writer.Flush()
		}},
	}
	foreign := map[string]string{CPAAuthKindHeader: "foreign-kind", CPAAuthProviderHeader: "foreign-provider", CPAAuthHostHeader: "foreign.example.test"}

	for _, entry := range entries {
		for _, state := range states {
			t.Run(entry.name+"/"+state.name, func(t *testing.T) {
				engine := gin.New()
				engine.Use(CPATraceIDMiddleware())
				engine.GET("/", func(c *gin.Context) {
					for name, value := range foreign {
						c.Writer.Header().Set(name, value)
					}
					state.set(c)
					entry.commit(c)
				})
				recorder := httptest.NewRecorder()
				engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
				if recorder.Code != http.StatusAccepted {
					t.Fatalf("status = %d, want %d", recorder.Code, http.StatusAccepted)
				}
				want := map[string]string{CPAAuthKindHeader: "", CPAAuthProviderHeader: "", CPAAuthHostHeader: ""}
				if state.confirmed {
					want = map[string]string{CPAAuthKindHeader: answered.Kind, CPAAuthProviderHeader: answered.Provider, CPAAuthHostHeader: answered.Host}
				}
				for name, value := range want {
					values, present := recorder.Header()[http.CanonicalHeaderKey(name)]
					if present != (value != "") || (present && (len(values) != 1 || values[0] != value)) {
						t.Fatalf("%s = %q (present %v), want %q", name, values, present, value)
					}
				}
			})
		}
	}
}

// A confirmation after the headers were committed changes nothing on that response.
func TestConfirmationAfterTheCommitHasNoEffect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(CPATraceIDMiddleware())
	engine.GET("/", func(c *gin.Context) {
		OfferGinCPAAuthStatement(c, AuthStatement{Kind: "oauth", Provider: "devin"})
		_, _ = c.Writer.Write([]byte("\n"))
		ConfirmGinCPAAuthOffer(c)
		_, _ = c.Writer.Write([]byte("answer"))
	})
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, name := range []string{CPAAuthKindHeader, CPAAuthProviderHeader, CPAAuthHostHeader} {
		if values, present := recorder.Header()[http.CanonicalHeaderKey(name)]; present {
			t.Fatalf("%s = %q on a response committed before the confirmation", name, values)
		}
	}
}
