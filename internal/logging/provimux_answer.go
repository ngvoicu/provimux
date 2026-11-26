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
// The switch models.answered-by as it is in force: the service sets it from each committed config (and keeps it off
// in Home mode). Off, nothing of this is written, and every answer is byte for byte what it is without this design,
// but for one thing: the six names are this gateway's own and are removed from a response whatever the switch, so an
// upstream gateway's (which no answer carried before these names existed) never reaches a client as if it were ours.

// The response headers of the statement, in the contract's order (prices-and-cache/openapi.yaml, components/headers).
// This build writes X-Provimux-Account, and where the gateway read what the attempt sent, X-Provimux-Model,
// X-Provimux-Model-Basis, X-Provimux-Model-Sent and X-Provimux-Entry; X-Provimux-Effort is a later step's (W5). Each
// is removed from a response where the statement has no value for it, so that an answer never carries another
// gateway's.
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
// marker, and what the attempt sent (answered-by.md 3.1 and 4, step W6a): the model id read from the request the
// gateway sent, and the providers.dev entry worked out from that request. (The model the vendor named and the effort
// are a later step's: W5.)
type ProvimuxAnswer struct {
	Account   string // "a." or "s." and 20 characters; "" names nobody and sends nothing
	ModelSent string // the model id read from the request the gateway sent to the vendor; "" = not read
	Entry     string // the providers.dev entry of this answer; "" = the gateway has none
}

// ProvimuxModelNotConfirmed is X-Provimux-Model-Basis where the vendor's answer named no model before the answer's
// first byte left (all this build knows): X-Provimux-Model is the id the gateway asked for.
const ProvimuxModelNotConfirmed = "not-confirmed"

// trimmed returns the answer with its marker trimmed, as the statement's other values are. A model and an entry are
// kept as they are: they are what was sent, and on the wire a space is written as %20, never dropped.
func (a ProvimuxAnswer) trimmed() ProvimuxAnswer {
	a.Account = strings.TrimSpace(a.Account)
	return a
}

// onTheWire returns the answer as it is sent (AuthStatement.OnTheWire): the zero answer when the marker is not a
// marker, and no entry without a model. It can be applied twice: the values are encoded only by Fields.
func (a ProvimuxAnswer) onTheWire() ProvimuxAnswer {
	if !provimuxMarkerOnTheWire.MatchString(a.Account) {
		return ProvimuxAnswer{}
	}
	if a.ModelSent == "" {
		a.Entry = ""
	}
	return a
}

// Fields returns the header name, the WebSocket field name and the value of each value of the answer that is sent,
// in the contract's order: none when Account is empty; the model, its basis and the sent model only together, and the
// entry only with them. A model and an entry go with each byte outside printable ASCII, a space or "%" written as "%"
// and two hex digits, as the provider is.
func (a ProvimuxAnswer) Fields() []ProvimuxField {
	a = a.onTheWire()
	if a.Account == "" {
		return nil
	}
	a.ModelSent, a.Entry = encodeProvider(a.ModelSent), encodeProvider(a.Entry)
	fields := []ProvimuxField{{Header: ProvimuxAccountHeader, Field: ProvimuxAccountField, Value: a.Account}}
	if a.ModelSent == "" {
		return fields
	}
	fields = append(fields,
		ProvimuxField{Header: ProvimuxModelHeader, Field: ProvimuxModelField, Value: a.ModelSent},
		ProvimuxField{Header: ProvimuxModelBasisHeader, Field: ProvimuxModelBasisField, Value: ProvimuxModelNotConfirmed},
		ProvimuxField{Header: ProvimuxModelSentHeader, Field: ProvimuxModelSentField, Value: a.ModelSent})
	if a.Entry != "" {
		fields = append(fields, ProvimuxField{Header: ProvimuxEntryHeader, Field: ProvimuxEntryField, Value: a.Entry})
	}
	return fields
}

// SetProvimuxAnswerHeaders sets, or deletes, every X-Provimux-* header of the statement from one statement as it is
// sent (AuthStatement.OnTheWire): a header the statement has no value for is deleted, so a value put in the header map
// earlier (a foreign writer, an upstream gateway's) never outlives the statement it belonged to. With models.answered-by
// off it writes none and deletes all six; on, a response that already exposes headers to browsers on another origin
// (Access-Control-Expose-Headers) exposes X-Provimux-Account too.
func SetProvimuxAnswerHeaders(header http.Header, statement AuthStatement) {
	if header == nil {
		return
	}
	values := map[string]string{}
	if ProvimuxAnsweredBy() {
		for _, field := range statement.Provimux.Fields() {
			values[field.Header] = field.Value
		}
		provimuxExpose(header)
	}
	for _, name := range provimuxFieldNames {
		if value := values[name.Header]; value != "" {
			header.Set(name.Header, value)
		} else {
			header.Del(name.Header)
		}
	}
}

// provimuxExposeHeader is the CORS response header that lets a browser on another origin read a response header.
const provimuxExposeHeader = "Access-Control-Expose-Headers"

// provimuxExpose adds X-Provimux-Account to Access-Control-Expose-Headers when the response has that header and it
// does not name ours yet.
func provimuxExpose(header http.Header) {
	exposed := header.Get(provimuxExposeHeader)
	if exposed == "" {
		return
	}
	for _, name := range strings.Split(exposed, ",") {
		if strings.EqualFold(strings.TrimSpace(name), ProvimuxAccountHeader) {
			return
		}
	}
	header.Set(provimuxExposeHeader, exposed+", "+ProvimuxAccountHeader)
}

// provimuxAnsweredBy holds models.answered-by as in force; false until a config sets it.
var provimuxAnsweredBy atomic.Bool

// SetProvimuxAnsweredBy puts models.answered-by in force and returns the previous value.
func SetProvimuxAnsweredBy(on bool) bool { return provimuxAnsweredBy.Swap(on) }

// ProvimuxAnsweredBy reports whether models.answered-by is on.
func ProvimuxAnsweredBy() bool { return provimuxAnsweredBy.Load() }
