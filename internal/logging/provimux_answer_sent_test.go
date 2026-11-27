package logging

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/origin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// Provimux: what the attempt sent, on the wire (answered-by.md 3.2): with a model read from the sent request, the
// statement carries X-Provimux-Model, X-Provimux-Model-Basis (not-confirmed: this build does not read the vendor's
// word) and X-Provimux-Model-Sent, and the entry only with them; never without the marker; a value outside printable
// ASCII, a space or "%" goes as "%" and two hex digits; X-Provimux-Effort is never written by this build. Ids are
// invented.

func TestProvimuxAnswerSentFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer ProvimuxAnswer
		want   map[string]string
	}{
		{"a model and an entry", ProvimuxAnswer{Account: provimuxTestMarker, ModelSent: "provimux-test-a", Entry: "anthropic/provimux-test-a"},
			map[string]string{ProvimuxAccountHeader: provimuxTestMarker, ProvimuxModelHeader: "provimux-test-a", ProvimuxModelBasisHeader: "not-confirmed",
				ProvimuxModelSentHeader: "provimux-test-a", ProvimuxEntryHeader: "anthropic/provimux-test-a"}},
		{"a model, no entry", ProvimuxAnswer{Account: provimuxTestMarker, ModelSent: "provimux-test-a"},
			map[string]string{ProvimuxAccountHeader: provimuxTestMarker, ProvimuxModelHeader: "provimux-test-a", ProvimuxModelBasisHeader: "not-confirmed",
				ProvimuxModelSentHeader: "provimux-test-a"}},
		{"an entry without a model is not sent", ProvimuxAnswer{Account: provimuxTestMarker, Entry: "anthropic/provimux-test-a"},
			map[string]string{ProvimuxAccountHeader: provimuxTestMarker}},
		{"no marker, nothing", ProvimuxAnswer{ModelSent: "provimux-test-a", Entry: "anthropic/provimux-test-a"}, map[string]string{}},
		{"encoded as the provider is", ProvimuxAnswer{Account: provimuxTestMarker, ModelSent: "provimux test%é", Entry: "openrouter/provimux test%é"},
			map[string]string{ProvimuxAccountHeader: provimuxTestMarker, ProvimuxModelHeader: "provimux%20test%25%C3%A9", ProvimuxModelBasisHeader: "not-confirmed",
				ProvimuxModelSentHeader: "provimux%20test%25%C3%A9", ProvimuxEntryHeader: "openrouter/provimux%20test%25%C3%A9"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := map[string]string{}
			var order []string
			for _, field := range tc.answer.Fields() {
				got[field.Header] = field.Value
				order = append(order, field.Header)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("fields %v, want %v", got, tc.want)
			}
			for name, value := range tc.want {
				if got[name] != value {
					t.Fatalf("%s = %q, want %q (all: %v)", name, got[name], value, got)
				}
			}
			// The contract's order.
			names := ProvimuxFieldNames()
			at := 0
			for _, header := range order {
				for at < len(names) && names[at].Header != header {
					at++
				}
				if at == len(names) {
					t.Fatalf("fields out of the contract's order: %v", order)
				}
			}
			// OnTheWire can be applied twice without changing what is sent.
			twice := AuthStatement{Kind: "apikey", Provider: "claude", Provimux: tc.answer}.OnTheWire().OnTheWire()
			again := map[string]string{}
			for _, field := range twice.Provimux.Fields() {
				again[field.Header] = field.Value
			}
			if len(again) != len(got) {
				t.Fatalf("twice on the wire: %v, once: %v", again, got)
			}
			for name, value := range got {
				if again[name] != value {
					t.Fatalf("twice on the wire: %s = %q, once %q", name, again[name], value)
				}
			}
		})
	}
}

// The header writer: the five values of a statement with a model, every foreign X-Provimux-* replaced or removed,
// X-Provimux-Effort among the removed.
func TestProvimuxAnswerSentHeaders(t *testing.T) {
	withProvimuxAnsweredBy(t, true)
	statement := AuthStatement{Kind: "apikey", Provider: "claude", Host: "gateway.example.test",
		Provimux: ProvimuxAnswer{Account: provimuxTestMarker, ModelSent: "provimux-test-a", Entry: "anthropic/provimux-test-a"}}
	header := serveProvimuxStatement(statement, true)
	want := map[string]string{ProvimuxAccountHeader: provimuxTestMarker, ProvimuxModelHeader: "provimux-test-a", ProvimuxModelBasisHeader: "not-confirmed",
		ProvimuxModelSentHeader: "provimux-test-a", ProvimuxEffortHeader: "", ProvimuxEntryHeader: "anthropic/provimux-test-a"}
	for name, value := range want {
		if got := header.Values(name); (value == "" && len(got) != 0) || (value != "" && (len(got) != 1 || got[0] != value)) {
			t.Fatalf("%s = %v, want %q", name, got, value)
		}
	}
	// Offered and not confirmed: none of the six, the foreign ones removed.
	header = serveProvimuxStatement(statement, false)
	for _, name := range ProvimuxFieldNames() {
		if values := header.Values(name.Header); len(values) != 0 {
			t.Fatalf("%s survives without a confirmed statement: %v", name.Header, values)
		}
	}
}

// On, a response that exposes headers to browsers on another origin exposes every X-Provimux-* name it writes, once;
// never one it does not write (no effort; no entry where there is none).
func TestProvimuxAnswerSentIsExposedToBrowsers(t *testing.T) {
	withProvimuxAnsweredBy(t, true)
	const exposed = "X-CPA-TRACE-ID, X-CPA-AUTH-KIND, X-CPA-AUTH-PROVIDER, X-CPA-AUTH-HOST"
	for _, test := range []struct {
		answer ProvimuxAnswer
		want   string
	}{
		{ProvimuxAnswer{Account: provimuxTestMarker, ModelSent: "provimux-test-a", Entry: "anthropic/provimux-test-a"},
			exposed + ", X-Provimux-Account, X-Provimux-Model, X-Provimux-Model-Basis, X-Provimux-Model-Sent, X-Provimux-Entry"},
		{ProvimuxAnswer{Account: provimuxTestMarker, ModelSent: "provimux-test-a"},
			exposed + ", X-Provimux-Account, X-Provimux-Model, X-Provimux-Model-Basis, X-Provimux-Model-Sent"},
		{ProvimuxAnswer{Account: provimuxTestMarker}, exposed + ", X-Provimux-Account"},
	} {
		header := http.Header{}
		header.Set("Access-Control-Expose-Headers", exposed)
		statement := AuthStatement{Kind: "apikey", Provider: "claude", Provimux: test.answer}
		SetProvimuxAnswerHeaders(header, statement)
		SetProvimuxAnswerHeaders(header, statement) // a second pass adds nothing
		if got := header.Get("Access-Control-Expose-Headers"); got != test.want {
			t.Fatalf("%+v: %q, want %q", test.answer, got, test.want)
		}
	}
}

// The record is handed a statement once, after the headers that carry it are written, and only the one they carried:
// headers committed first without a statement (a keep-alive) hand nothing on, whatever is confirmed after; a statement
// only offered hands nothing on. The record's values are never on the wire.
func TestProvimuxAnswerNotedOnlyWhenWritten(t *testing.T) {
	t.Cleanup(origin.ShippedGatewayForTests(true)) // the gateway as shipped (the process mark)
	withProvimuxAnsweredBy(t, true)
	var noted []AuthStatement
	SetProvimuxAnsweredWritten(func(s AuthStatement) { noted = append(noted, s) })
	t.Cleanup(func() { SetProvimuxAnsweredWritten(nil) })
	statement := AuthStatement{Kind: "apikey", Provider: "claude", Host: "gateway.example.test",
		Provimux: ProvimuxAnswer{Account: provimuxTestMarker, ModelSent: "provimux-test-a", Entry: "anthropic/provimux-test-a", Credential: "provimux-test-cred", Name: "provimux-test-name", Client: "k.client"}}
	serve := func(handler func(c *gin.Context)) http.Header {
		gin.SetMode(gin.TestMode)
		router := gin.New()
		router.Use(CPATraceIDMiddleware())
		router.GET("/", handler)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
		return recorder.Header()
	}
	header := serve(func(c *gin.Context) {
		OfferGinCPAAuthStatement(c, statement)
		ConfirmGinCPAAuthOffer(c)
		c.String(http.StatusOK, "answer")
		c.String(http.StatusOK, " more")
	})
	if len(noted) != 1 || noted[0].Provimux != statement.Provimux {
		t.Fatalf("a written answer: noted %+v", noted)
	}
	for name := range header {
		for _, value := range header.Values(name) {
			if strings.Contains(value, "provimux-test-cred") || strings.Contains(value, "provimux-test-name") || strings.Contains(value, "k.client") {
				t.Fatalf("the record's values are on the wire: %s: %s", name, value)
			}
		}
	}
	noted = nil
	serve(func(c *gin.Context) {
		c.Writer.WriteHeaderNow() // a keep-alive commits the headers first
		c.Writer.Flush()
		OfferGinCPAAuthStatement(c, statement)
		ConfirmGinCPAAuthOffer(c)
		c.String(http.StatusOK, "answer")
	})
	serve(func(c *gin.Context) {
		OfferGinCPAAuthStatement(c, statement) // offered, never confirmed
		c.String(http.StatusOK, "answer")
	})
	if len(noted) != 0 {
		t.Fatalf("noted %+v without the statement on the written headers", noted)
	}
	// In a process Provimux's own main did not start: nothing (the process mark).
	restore := origin.ShippedGatewayForTests(false)
	serve(func(c *gin.Context) {
		OfferGinCPAAuthStatement(c, statement)
		ConfirmGinCPAAuthOffer(c)
		c.String(http.StatusOK, "answer")
	})
	restore()
	if len(noted) != 0 {
		t.Fatalf("noted %+v without the process mark", noted)
	}
}
