package logging

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpwire"
	"golang.org/x/net/http/httpguts"
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

type cpaTraceState struct {
	mu       sync.RWMutex
	traceID  string
	authKind string
	// authProvider and authHost name the same credential as authKind; each selection replaces all three.
	authProvider string
	authHost     string
}

func (s *cpaTraceState) setAuthProvider(provider string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.authProvider = strings.TrimSpace(provider)
	s.mu.Unlock()
}

func (s *cpaTraceState) setAuthHost(host string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.authHost = strings.TrimSpace(host)
	s.mu.Unlock()
}

func (s *cpaTraceState) getAuthProviderAndHost() (string, string) {
	if s == nil {
		return "", ""
	}
	s.mu.RLock()
	provider, host := s.authProvider, s.authHost
	s.mu.RUnlock()
	return provider, host
}

func (s *cpaTraceState) setAuthKind(kind string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.authKind = strings.TrimSpace(kind)
	s.mu.Unlock()
}

func (s *cpaTraceState) getAuthKind() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	kind := s.authKind
	s.mu.RUnlock()
	return kind
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

// GinCPAAuthKindCallback returns a callback, safe to invoke after the Gin context is released, that stores the kind
// of the selected credential. A later call replaces an earlier one, so a retry on another credential before the
// response headers are committed reports the credential that answered.
func GinCPAAuthKindCallback(c *gin.Context) func(string) {
	state := ginCPATraceState(c)
	if state == nil {
		return nil
	}
	return state.setAuthKind
}

// SetGinCPAAuthKind stores the selected credential's kind until the downstream response headers are committed.
func SetGinCPAAuthKind(c *gin.Context, kind string) {
	if callback := GinCPAAuthKindCallback(c); callback != nil {
		callback(kind)
	}
}

// GetGinCPAAuthKind returns the credential kind stored for the current request.
func GetGinCPAAuthKind(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, exists := c.Get(ginCPATraceStateKey)
	if !exists {
		return ""
	}
	state, _ := value.(*cpaTraceState)
	return state.getAuthKind()
}

// GinCPAAuthProviderCallback returns a callback, safe to invoke after the Gin context is released, that stores the
// provider of the selected credential. Like the kind, a later call replaces an earlier one.
func GinCPAAuthProviderCallback(c *gin.Context) func(string) {
	state := ginCPATraceState(c)
	if state == nil {
		return nil
	}
	return state.setAuthProvider
}

// GinCPAAuthHostCallback returns a callback, safe to invoke after the Gin context is released, that stores the base
// URL host of the selected API key, "" for a login or a key without one. Like the kind, a later call replaces an
// earlier one, so a retry on a login clears the host of a key tried before it.
func GinCPAAuthHostCallback(c *gin.Context) func(string) {
	state := ginCPATraceState(c)
	if state == nil {
		return nil
	}
	return state.setAuthHost
}

// SetGinCPAAuthProvider stores the selected credential's provider and base URL host ("" when it has none) until the
// downstream response headers are committed.
func SetGinCPAAuthProvider(c *gin.Context, provider, host string) {
	if callback := GinCPAAuthProviderCallback(c); callback != nil {
		callback(provider)
	}
	if callback := GinCPAAuthHostCallback(c); callback != nil {
		callback(host)
	}
}

// GetGinCPAAuthProvider returns the credential provider and base URL host stored for the current request.
func GetGinCPAAuthProvider(c *gin.Context) (string, string) {
	if c == nil {
		return "", ""
	}
	value, exists := c.Get(ginCPATraceStateKey)
	if !exists {
		return "", ""
	}
	state, _ := value.(*cpaTraceState)
	return state.getAuthProviderAndHost()
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
	if kind := w.state.getAuthKind(); kind != "" {
		w.ResponseWriter.Header().Set(CPAAuthKindHeader, kind)
	}
	// A provider or host that is not a valid header field value would make an HTTP/1.1 client reject the whole
	// response: it is not written, and without a provider no host is.
	provider, host := w.state.getAuthProviderAndHost()
	if provider == "" || !httpguts.ValidHeaderFieldValue(provider) {
		return
	}
	w.ResponseWriter.Header().Set(CPAAuthProviderHeader, provider)
	if host != "" && httpguts.ValidHeaderFieldValue(host) {
		w.ResponseWriter.Header().Set(CPAAuthHostHeader, host)
	}
}
