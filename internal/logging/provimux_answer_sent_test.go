package logging

import (
	"testing"
)

// Provimux: what the attempt sent, on the wire (answered-by.md 3.2, step W6a): with a model read from the sent
// request, the statement carries X-Provimux-Model, X-Provimux-Model-Basis (not-confirmed: the vendor's word is W5's)
// and X-Provimux-Model-Sent, and the entry only with them; never without the marker; a value outside printable ASCII,
// a space or "%" goes as "%" and two hex digits; X-Provimux-Effort is never written by this build. Ids are invented.

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
