package logging

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpwire"
)

// CPATraceIDHeader is the downstream response header used to correlate requests with selected credentials.
const CPATraceIDHeader = "X-CPA-TRACE-ID"

// CPAAuthKindHeader is the downstream response header naming the kind of credential that served the request:
// "oauth" for a subscription login, "apikey" for an API key.
const CPAAuthKindHeader = "X-CPA-AUTH-KIND"

// CPAAuthProviderHeader is the downstream response header naming the provider of the credential that served the
// request, as the model accounts list gives it: two credentials of one kind serving one model (a Claude login and a
// Devin login) are different services, with prompt caches that are not shared.
const CPAAuthProviderHeader = "X-CPA-AUTH-PROVIDER"

// CPAAuthHostHeader is the downstream response header naming the host of the base URL of the API key that served the
// request, as the model accounts list gives it; absent for a login or a key without a base URL.
const CPAAuthHostHeader = "X-CPA-AUTH-HOST"

const ginCPATraceStateKey = "__cpa_trace_state__"

// FormatCPATraceID builds a CPA trace ID from the selection time, auth index, and request ID.
func FormatCPATraceID(selectedAt time.Time, authIndex, requestID string) string {
	authIndex = strings.TrimSpace(authIndex)
	requestID = strings.TrimSpace(requestID)
	if selectedAt.IsZero() || authIndex == "" || requestID == "" {
		return ""
	}
	return selectedAt.Format("20060102150405") + "-" + authIndex + "-" + requestID
}

// AuthStatement says which credential produced a response: its kind ("oauth" or "apikey"), its provider and, for an
// API key with a base URL, the host. It is one statement, set by one call and read as one: a response carries the
// whole statement or none of it.
type AuthStatement struct {
	Kind     string
	Provider string
	Host     string
	Provimux ProvimuxAnswer // Provimux: the account's marker (answered-by.md); sent only with the three
}

// OnTheWire returns the statement as every transport sends it, response headers and websocket fields alike: whole,
// or the zero statement. It is whole when the kind is "oauth" or "apikey", there is a provider, and the host is empty
// or printable ASCII (Auth.AuthStatement gives an internationalised host in its IDNA form). The provider is sent byte
// for byte, except that a byte outside printable ASCII, a space or "%" is written as "%" and two upper-case hex digits:
// every built-in provider name goes out unchanged, and a header and a JSON field carry the same value. The kind is
// never sent alone: a client reads a kind without a provider as an older proxy and guesses the credential.
func (s AuthStatement) OnTheWire() AuthStatement {
	if (s.Kind != "oauth" && s.Kind != "apikey") || s.Provider == "" || !printableASCII(s.Host) {
		return AuthStatement{}
	}
	return AuthStatement{Kind: s.Kind, Provider: encodeProvider(s.Provider), Host: s.Host, Provimux: s.Provimux.onTheWire()}
}

// encodeProvider writes each byte <= 0x20, >= 0x7F or "%" of a provider as "%" and two upper-case hex digits.
func encodeProvider(provider string) string {
	const hex = "0123456789ABCDEF"
	var encoded strings.Builder
	for i := 0; i < len(provider); i++ {
		b := provider[i]
		if b <= 0x20 || b >= 0x7f || b == '%' {
			encoded.WriteByte('%')
			encoded.WriteByte(hex[b>>4])
			encoded.WriteByte(hex[b&0x0f])
			continue
		}
		encoded.WriteByte(b)
	}
	return encoded.String()
}

func printableASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

type cpaTraceState struct {
	mu      sync.RWMutex
	traceID string
	// offered is the statement handed over with the result the execution layer last returned on this request. It
	// names nothing by itself: a route confirms it where it writes that result.
	offered AuthStatement
	// confirmed is the statement a route confirmed at the write of its answer's first byte: the only statement the
	// response headers carry.
	confirmed AuthStatement
}

func trimmedAuthStatement(statement AuthStatement) AuthStatement {
	return AuthStatement{Kind: strings.TrimSpace(statement.Kind), Provider: strings.TrimSpace(statement.Provider), Host: strings.TrimSpace(statement.Host), Provimux: statement.Provimux.trimmed()}
}

func (s *cpaTraceState) offer(statement AuthStatement) {
	if s == nil {
		return
	}
	statement = trimmedAuthStatement(statement)
	s.mu.Lock()
	s.offered = statement
	s.confirmed = AuthStatement{}
	s.mu.Unlock()
}

func (s *cpaTraceState) confirmOffer() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.confirmed = s.offered
	s.mu.Unlock()
}

func (s *cpaTraceState) confirm(statement AuthStatement) {
	if s == nil {
		return
	}
	statement = trimmedAuthStatement(statement)
	s.mu.Lock()
	s.confirmed = statement
	s.mu.Unlock()
}

func (s *cpaTraceState) getConfirmed() AuthStatement {
	if s == nil {
		return AuthStatement{}
	}
	s.mu.RLock()
	statement := s.confirmed
	s.mu.RUnlock()
	return statement
}

func (s *cpaTraceState) set(traceID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.traceID = strings.TrimSpace(traceID)
	s.mu.Unlock()
}

func (s *cpaTraceState) get() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	traceID := s.traceID
	s.mu.RUnlock()
	return traceID
}

func ginCPATraceState(c *gin.Context) *cpaTraceState {
	if c == nil {
		return nil
	}
	if value, exists := c.Get(ginCPATraceStateKey); exists {
		if state, ok := value.(*cpaTraceState); ok && state != nil {
			return state
		}
	}
	state := &cpaTraceState{}
	c.Set(ginCPATraceStateKey, state)
	return state
}

// GinCPATraceIDCallback returns a callback that is safe to invoke after the Gin context is released.
func GinCPATraceIDCallback(c *gin.Context) func(string) {
	state := ginCPATraceState(c)
	if state == nil {
		return nil
	}
	requestID := GetGinRequestID(c)
	if requestID == "" && c.Request != nil {
		requestID = GetRequestID(c.Request.Context())
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil
	}
	return func(authIndex string) {
		if traceID := FormatCPATraceID(time.Now(), authIndex, requestID); traceID != "" {
			state.set(traceID)
		}
	}
}

// SetGinCPATraceID stores the trace ID until the downstream response headers are committed.
func SetGinCPATraceID(c *gin.Context, authIndex string) {
	if callback := GinCPATraceIDCallback(c); callback != nil {
		callback(authIndex)
	}
}

// OfferGinCPAAuthStatement hands over the statement of the result the execution layer returns on this request (an
// empty statement withdraws an earlier one). An offer names nothing: it withdraws any confirmed statement, and the
// response headers carry it only once a route confirms it with ConfirmGinCPAAuthOffer.
func OfferGinCPAAuthStatement(c *gin.Context, statement AuthStatement) {
	ginCPATraceState(c).offer(statement)
}

// ConfirmGinCPAAuthOffer confirms the statement last offered on this request. A route calls it immediately before it
// writes the first byte of the result that statement was offered with, with nothing between that can write: the
// response headers committed by that write carry the statement. After the headers are committed it has no effect.
func ConfirmGinCPAAuthOffer(c *gin.Context) {
	ginCPATraceState(c).confirmOffer()
}

// ConfirmGinCPAAuthStatement confirms the statement of a credential a route holds itself (a relay that selected it),
// immediately before the route writes that credential's upstream response.
func ConfirmGinCPAAuthStatement(c *gin.Context, statement AuthStatement) {
	ginCPATraceState(c).confirm(statement)
}

// GetGinCPAAuthStatement returns the credential statement confirmed for the current request.
func GetGinCPAAuthStatement(c *gin.Context) AuthStatement {
	if c == nil {
		return AuthStatement{}
	}
	value, exists := c.Get(ginCPATraceStateKey)
	if !exists {
		return AuthStatement{}
	}
	state, _ := value.(*cpaTraceState)
	return state.getConfirmed()
}

// GetGinCPATraceID returns the trace ID stored for the current request.
func GetGinCPATraceID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, exists := c.Get(ginCPATraceStateKey)
	if !exists {
		return ""
	}
	state, _ := value.(*cpaTraceState)
	return state.get()
}

// CPATraceIDMiddleware injects a stored trace ID immediately before response headers are committed.
func CPATraceIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		state := ginCPATraceState(c)
		c.Writer = &cpaTraceResponseWriter{ResponseWriter: c.Writer, state: state}
		c.Next()
	}
}

type cpaTraceResponseWriter struct {
	gin.ResponseWriter
	state *cpaTraceState

	provimux provimuxWritten // Provimux: the statement written, for the record (answered-by.md 4.4)
}

func (w *cpaTraceResponseWriter) Unwrap() http.ResponseWriter {
	if w == nil {
		return nil
	}
	return w.ResponseWriter
}

func (w *cpaTraceResponseWriter) WriteHeader(statusCode int) {
	w.applyTraceHeader()
	defer w.provimuxNote() // Provimux: the record, once the headers are written
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *cpaTraceResponseWriter) WriteHeaderNow() {
	w.applyTraceHeader()
	defer w.provimuxNote() // Provimux: the record, once the headers are written
	w.ResponseWriter.WriteHeaderNow()
}

func (w *cpaTraceResponseWriter) Write(data []byte) (int, error) {
	w.applyTraceHeader()
	defer w.provimuxNote() // Provimux: the record, once the headers are written
	return w.ResponseWriter.Write(data)
}

func (w *cpaTraceResponseWriter) WriteString(data string) (int, error) {
	w.applyTraceHeader()
	defer w.provimuxNote() // Provimux: the record, once the headers are written
	return w.ResponseWriter.WriteString(data)
}

func (w *cpaTraceResponseWriter) Flush() {
	_ = w.FlushError()
}

// FlushError preserves trace headers while forwarding transport errors.
func (w *cpaTraceResponseWriter) FlushError() error {
	w.applyTraceHeader()
	defer w.provimuxNote() // Provimux: the record, once the headers are written
	return httpwire.FlushResponse(w.ResponseWriter)
}

func (w *cpaTraceResponseWriter) applyTraceHeader() {
	if w == nil || w.ResponseWriter == nil || w.ResponseWriter.Written() {
		return
	}
	if traceID := w.state.get(); traceID != "" {
		w.ResponseWriter.Header().Set(CPATraceIDHeader, traceID)
	}
	// One read: the kind, provider and host written here are one credential's confirmed statement, as sent on the
	// wire. Every pass sets or deletes all three, so a value put in the header map earlier (a staged status, a foreign
	// writer) never outlives the statement it belonged to.
	statement := w.state.getConfirmed().OnTheWire()
	for name, value := range map[string]string{CPAAuthKindHeader: statement.Kind, CPAAuthProviderHeader: statement.Provider, CPAAuthHostHeader: statement.Host} {
		if value != "" {
			w.ResponseWriter.Header().Set(name, value)
		} else {
			w.ResponseWriter.Header().Del(name)
		}
	}
	SetProvimuxAnswerHeaders(w.ResponseWriter.Header(), statement) // Provimux: from the same statement (answered-by.md 5)
	w.provimux.applied = statement
}
