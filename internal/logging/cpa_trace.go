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
}

// OnTheWire returns the statement as every transport sends it, response headers and websocket fields alike. The
// kind is sent as it is. The provider and the host are sent only if both are plain printable ASCII (which is also a
// valid header field value) and there is a provider; otherwise neither is, and a client falls back to the kind. A
// value outside ASCII would be read differently by different clients (UTF-8 octets in a header, text in JSON), so
// one credential would look like two services depending on the transport.
func (s AuthStatement) OnTheWire() AuthStatement {
	if s.Provider == "" || !printableASCII(s.Provider) || !printableASCII(s.Host) {
		return AuthStatement{Kind: s.Kind}
	}
	return s
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
	// authStatement names the credential whose upstream produced the response; empty until one has.
	authStatement AuthStatement
}

func (s *cpaTraceState) setAuthStatement(kind, provider, host string) {
	if s == nil {
		return
	}
	statement := AuthStatement{Kind: strings.TrimSpace(kind), Provider: strings.TrimSpace(provider), Host: strings.TrimSpace(host)}
	s.mu.Lock()
	s.authStatement = statement
	s.mu.Unlock()
}

func (s *cpaTraceState) getAuthStatement() AuthStatement {
	if s == nil {
		return AuthStatement{}
	}
	s.mu.RLock()
	statement := s.authStatement
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

// GinCPAAuthStatementCallback returns a callback, safe to invoke after the Gin context is released, that stores in
// one step the statement of the credential whose upstream produced the response: kind, provider and host. A later
// call replaces an earlier one; a call with three empty values withdraws it.
func GinCPAAuthStatementCallback(c *gin.Context) func(kind, provider, host string) {
	state := ginCPATraceState(c)
	if state == nil {
		return nil
	}
	return state.setAuthStatement
}

// SetGinCPAAuthStatement stores the statement of the credential whose upstream produced the response until the
// downstream response headers are committed.
func SetGinCPAAuthStatement(c *gin.Context, statement AuthStatement) {
	if callback := GinCPAAuthStatementCallback(c); callback != nil {
		callback(statement.Kind, statement.Provider, statement.Host)
	}
}

// GetGinCPAAuthStatement returns the credential statement stored for the current request.
func GetGinCPAAuthStatement(c *gin.Context) AuthStatement {
	if c == nil {
		return AuthStatement{}
	}
	value, exists := c.Get(ginCPATraceStateKey)
	if !exists {
		return AuthStatement{}
	}
	state, _ := value.(*cpaTraceState)
	return state.getAuthStatement()
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
}

func (w *cpaTraceResponseWriter) Unwrap() http.ResponseWriter {
	if w == nil {
		return nil
	}
	return w.ResponseWriter
}

func (w *cpaTraceResponseWriter) WriteHeader(statusCode int) {
	w.applyTraceHeader()
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *cpaTraceResponseWriter) WriteHeaderNow() {
	w.applyTraceHeader()
	w.ResponseWriter.WriteHeaderNow()
}

func (w *cpaTraceResponseWriter) Write(data []byte) (int, error) {
	w.applyTraceHeader()
	return w.ResponseWriter.Write(data)
}

func (w *cpaTraceResponseWriter) WriteString(data string) (int, error) {
	w.applyTraceHeader()
	return w.ResponseWriter.WriteString(data)
}

func (w *cpaTraceResponseWriter) Flush() {
	_ = w.FlushError()
}

// FlushError preserves trace headers while forwarding transport errors.
func (w *cpaTraceResponseWriter) FlushError() error {
	w.applyTraceHeader()
	return httpwire.FlushResponse(w.ResponseWriter)
}

func (w *cpaTraceResponseWriter) applyTraceHeader() {
	if w == nil || w.ResponseWriter == nil || w.ResponseWriter.Written() {
		return
	}
	if traceID := w.state.get(); traceID != "" {
		w.ResponseWriter.Header().Set(CPATraceIDHeader, traceID)
	}
	// One read: the kind, provider and host written here are one credential's statement, as sent on the wire.
	statement := w.state.getAuthStatement().OnTheWire()
	for name, value := range map[string]string{CPAAuthKindHeader: statement.Kind, CPAAuthProviderHeader: statement.Provider, CPAAuthHostHeader: statement.Host} {
		if value != "" {
			w.ResponseWriter.Header().Set(name, value)
		}
	}
}
