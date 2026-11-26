package logging

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// Provimux: the marker is a value of the statement and goes on the wire only with it (answered-by.md 3.2 and 5): the
// headers a write commits carry X-Provimux-Account exactly when they carry the three X-CPA-AUTH-* headers of the
// same confirmed statement; a foreign X-Provimux-* header never survives; with models.answered-by off nothing is
// touched. The markers below are invented.

const provimuxTestMarker = "a.abcdefghijklmnopqrst"

// provimuxForeign is what an upstream gateway, or an earlier writer, may have put in the header map.
var provimuxForeign = map[string]string{
	ProvimuxAccountHeader: "a.zzzzzzzzzzzzzzzzzzzz", ProvimuxModelHeader: "foreign-model", ProvimuxModelBasisHeader: "vendor",
	ProvimuxModelSentHeader: "foreign-model", ProvimuxEffortHeader: "high", ProvimuxEntryHeader: "foreign/model",
}

func withProvimuxAnsweredBy(t *testing.T, on bool) {
	t.Helper()
	was := SetProvimuxAnsweredBy(on)
	t.Cleanup(func() { SetProvimuxAnsweredBy(was) })
}

// serveProvimuxStatement answers one request: the foreign headers set first, then the statement offered and
// confirmed (unless confirm is false), then one write.
func serveProvimuxStatement(statement AuthStatement, confirm bool) http.Header {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CPATraceIDMiddleware())
	router.GET("/", func(c *gin.Context) {
		for name, value := range provimuxForeign {
			c.Header(name, value)
		}
		OfferGinCPAAuthStatement(c, statement)
		if confirm {
			ConfirmGinCPAAuthOffer(c)
		}
		c.String(http.StatusOK, "answer")
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	return recorder.Header()
}

func TestProvimuxAnswerGoesOnlyWithItsStatement(t *testing.T) {
	withProvimuxAnsweredBy(t, true)
	whole := AuthStatement{Kind: "apikey", Provider: "claude", Host: "gateway.example.test", Provimux: ProvimuxAnswer{Account: provimuxTestMarker}}
	for _, test := range []struct {
		name      string
		statement AuthStatement
		confirm   bool
		named     bool
		account   string
	}{
		{name: "a confirmed whole statement", statement: whole, confirm: true, named: true, account: provimuxTestMarker},
		{name: "a marker with spaces around it", statement: AuthStatement{Kind: "oauth", Provider: "codex", Provimux: ProvimuxAnswer{Account: " " + provimuxTestMarker + "\t"}}, confirm: true, named: true, account: provimuxTestMarker},
		{name: "a statement offered, never confirmed", statement: whole, confirm: false},
		{name: "a marker without a whole statement", statement: AuthStatement{Kind: "apikey", Provimux: ProvimuxAnswer{Account: provimuxTestMarker}}, confirm: true},
		{name: "a statement without a marker", statement: AuthStatement{Kind: "oauth", Provider: "codex"}, confirm: true, named: true},
		{name: "a value that is not a marker", statement: AuthStatement{Kind: "oauth", Provider: "codex", Provimux: ProvimuxAnswer{Account: "a.ABCDEFGHIJKLMNOPQRST"}}, confirm: true, named: true},
		{name: "nobody", confirm: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := serveProvimuxStatement(test.statement, test.confirm)
			if named := header.Get(CPAAuthKindHeader) != ""; named != test.named {
				t.Fatalf("X-CPA-AUTH-KIND %q, want named %v", header.Get(CPAAuthKindHeader), test.named)
			}
			if got := header.Get(ProvimuxAccountHeader); got != test.account {
				t.Fatalf("X-Provimux-Account %q, want %q", got, test.account)
			}
			for _, name := range ProvimuxFieldNames()[1:] {
				if values := header.Values(name.Header); len(values) != 0 {
					t.Fatalf("%s survives: %v", name.Header, values)
				}
			}
		})
	}
}

// With models.answered-by off the writer writes no marker, and still removes another gateway's X-Provimux-* headers:
// they are this gateway's names, and a marker that is not ours never reaches a client. The three are the owner's.
func TestProvimuxAnswerSwitchOffWritesNoneAndPassesNoneOfAnothers(t *testing.T) {
	withProvimuxAnsweredBy(t, false)
	header := serveProvimuxStatement(AuthStatement{Kind: "apikey", Provider: "claude", Host: "gateway.example.test", Provimux: ProvimuxAnswer{Account: provimuxTestMarker}}, true)
	if header.Get(CPAAuthKindHeader) != "apikey" {
		t.Fatalf("the three are the owner's patch's: kind %q", header.Get(CPAAuthKindHeader))
	}
	for _, name := range ProvimuxFieldNames() {
		if got := header.Values(name.Header); len(got) != 0 {
			t.Fatalf("%s = %v with the switch off", name.Header, got)
		}
	}
}

// On, a response that exposes headers to browsers on another origin exposes X-Provimux-Account too, once; off, the
// value is left as it was set.
func TestProvimuxAnswerIsExposedToBrowsersWhenOn(t *testing.T) {
	const exposed = "X-CPA-TRACE-ID, X-CPA-AUTH-KIND, X-CPA-AUTH-PROVIDER, X-CPA-AUTH-HOST"
	statement := AuthStatement{Kind: "apikey", Provider: "claude", Host: "gateway.example.test", Provimux: ProvimuxAnswer{Account: provimuxTestMarker}}
	for _, test := range []struct {
		on     bool
		before string
		want   string
	}{
		{on: true, before: exposed, want: exposed + ", X-Provimux-Account"},
		{on: true, before: exposed + ", x-provimux-account", want: exposed + ", x-provimux-account"},
		{on: true, before: "", want: ""},
		{on: false, before: exposed, want: exposed},
	} {
		withProvimuxAnsweredBy(t, test.on)
		header := http.Header{}
		if test.before != "" {
			header.Set("Access-Control-Expose-Headers", test.before)
		}
		SetProvimuxAnswerHeaders(header, statement)
		SetProvimuxAnswerHeaders(header, statement) // a second pass adds nothing
		if got := header.Get("Access-Control-Expose-Headers"); got != test.want {
			t.Fatalf("on %v, before %q: %q, want %q", test.on, test.before, got, test.want)
		}
	}
}

func TestProvimuxAnswerFields(t *testing.T) {
	if fields := (ProvimuxAnswer{}).Fields(); fields != nil {
		t.Fatalf("an empty answer sends %v", fields)
	}
	fields := ProvimuxAnswer{Account: provimuxTestMarker}.Fields()
	if len(fields) != 1 || fields[0] != (ProvimuxField{Header: "X-Provimux-Account", Field: "provimux_account", Value: provimuxTestMarker}) {
		t.Fatalf("fields %+v", fields)
	}
	names := ProvimuxFieldNames()
	want := []string{"X-Provimux-Account", "X-Provimux-Model", "X-Provimux-Model-Basis", "X-Provimux-Model-Sent", "X-Provimux-Effort", "X-Provimux-Entry"}
	wantFields := []string{"provimux_account", "provimux_model", "provimux_model_basis", "provimux_model_sent", "provimux_effort", "provimux_entry"}
	for i := range want {
		if names[i].Header != want[i] || names[i].Field != wantFields[i] || names[i].Value != "" {
			t.Fatalf("name %d: %+v, want %s %s (the contract's order)", i, names[i], want[i], wantFields[i])
		}
	}
}
