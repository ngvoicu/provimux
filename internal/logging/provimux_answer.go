package logging

import (
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/MoeForge/moego/regexp"
)

// Provimux: who answered (.github/provimux/design/answered-by.md). What Provimux adds to the statement of the
// credential that produced a response, and how it goes on the wire: the X-Provimux-* response headers, and on the
// Responses WebSocket the provimux_* fields of a turn's response.created. It is a field of AuthStatement, so it is
// captured, offered, withdrawn, confirmed and written with the statement, and never apart from it: no place writes it
// that does not write the three X-CPA-AUTH-* headers, and an answer that names nobody carries none of it.
//
// The switch models.answered-by as it is in force: the service sets it from each committed config. Off, nothing here
// is written, read or removed, and every answer is byte for byte what it is without this design.

// The response headers of the statement, in the contract's order (prices-and-cache/openapi.yaml, components/headers).
// This build writes X-Provimux-Account; the five others are the contract's, written by later steps, and are removed
// from a response like it, so that an answer never carries another gateway's.
const (
	ProvimuxAccountHeader    = "X-Provimux-Account"
	ProvimuxModelHeader      = "X-Provimux-Model"
	ProvimuxModelBasisHeader = "X-Provimux-Model-Basis"
	ProvimuxModelSentHeader  = "X-Provimux-Model-Sent"
	ProvimuxEffortHeader     = "X-Provimux-Effort"
	ProvimuxEntryHeader      = "X-Provimux-Entry"
)

// The fields of response.created on the Responses WebSocket, the same values as the headers above.
const (
	ProvimuxAccountField    = "provimux_account"
	ProvimuxModelField      = "provimux_model"
	ProvimuxModelBasisField = "provimux_model_basis"
	ProvimuxModelSentField  = "provimux_model_sent"
	ProvimuxEffortField     = "provimux_effort"
	ProvimuxEntryField      = "provimux_entry"
)

// ProvimuxField is one value of the statement on the wire: its header name, its WebSocket field name and its value.
type ProvimuxField struct {
	Header string
	Field  string
	Value  string
}

// provimuxFieldNames are the header and field names of the whole statement, in the contract's order.
var provimuxFieldNames = []ProvimuxField{
	{Header: ProvimuxAccountHeader, Field: ProvimuxAccountField},
	{Header: ProvimuxModelHeader, Field: ProvimuxModelField},
	{Header: ProvimuxModelBasisHeader, Field: ProvimuxModelBasisField},
	{Header: ProvimuxModelSentHeader, Field: ProvimuxModelSentField},
	{Header: ProvimuxEffortHeader, Field: ProvimuxEffortField},
	{Header: ProvimuxEntryHeader, Field: ProvimuxEntryField},
}

// ProvimuxFieldNames returns the header and field names of the whole statement (no values), in the contract's order:
// what is removed from a response before this gateway's own values are written.
func ProvimuxFieldNames() []ProvimuxField { return append([]ProvimuxField(nil), provimuxFieldNames...) }

// provimuxMarkerOnTheWire is the form of an account marker (answered-by.md 2.2): plain ASCII, nothing to encode.
var provimuxMarkerOnTheWire = regexp.MustCompile(`^[as]\.[a-z2-7]{20}$`)

// ProvimuxAnswer is what Provimux adds to the statement of the credential that produced a response: the account's
// marker. (The model the attempt knew of, and the answer's entry, are later steps: answered-by.md W5 and W6.)
type ProvimuxAnswer struct {
	Account string // "a." or "s." and 20 characters; "" names nobody and sends nothing
}

// trimmed returns the answer with its values trimmed, as the statement's other values are.
func (a ProvimuxAnswer) trimmed() ProvimuxAnswer {
	return ProvimuxAnswer{Account: strings.TrimSpace(a.Account)}
}

// onTheWire returns the answer as it is sent: whole, or the zero answer when the marker is not a marker.
func (a ProvimuxAnswer) onTheWire() ProvimuxAnswer {
	if !provimuxMarkerOnTheWire.MatchString(a.Account) {
		return ProvimuxAnswer{}
	}
	return a
}

// Fields returns the header name, the WebSocket field name and the value of each value of the answer that is sent,
// in a fixed order: none when Account is empty.
func (a ProvimuxAnswer) Fields() []ProvimuxField {
	a = a.onTheWire()
	if a.Account == "" {
		return nil
	}
	return []ProvimuxField{{Header: ProvimuxAccountHeader, Field: ProvimuxAccountField, Value: a.Account}}
}

// SetProvimuxAnswerHeaders sets, or deletes, every X-Provimux-* header of the statement from one statement as it is
// sent (AuthStatement.OnTheWire): a header the statement has no value for is deleted, so a value put in the header map
// earlier (a foreign writer, an upstream gateway's) never outlives the statement it belonged to. With models.answered-by
// off it touches nothing.
func SetProvimuxAnswerHeaders(header http.Header, statement AuthStatement) {
	if header == nil || !ProvimuxAnsweredBy() {
		return
	}
	values := map[string]string{}
	for _, field := range statement.Provimux.Fields() {
		values[field.Header] = field.Value
	}
	for _, name := range provimuxFieldNames {
		if value := values[name.Header]; value != "" {
			header.Set(name.Header, value)
		} else {
			header.Del(name.Header)
		}
	}
}

// provimuxAnsweredBy holds models.answered-by as in force; false until a config sets it.
var provimuxAnsweredBy atomic.Bool

// SetProvimuxAnsweredBy puts models.answered-by in force and returns the previous value.
func SetProvimuxAnsweredBy(on bool) bool { return provimuxAnsweredBy.Swap(on) }

// ProvimuxAnsweredBy reports whether models.answered-by is on.
func ProvimuxAnsweredBy() bool { return provimuxAnsweredBy.Load() }
