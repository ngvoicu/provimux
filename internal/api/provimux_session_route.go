package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/onlymynames"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/sessionroute"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coresession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
)

// Provimux: the client paths of session-route (.github/provimux/design/session-route.md 4, contract
// session-route/openapi.yaml). A client key shows and releases its own conversation's binding for a name, named by the
// session headers and body fields its turns carry. Stock CLIProxyAPI has neither path: a 404 means "not this build".

// provimuxSessionRouteBodyLimit bounds the request body: a name, an identity object and a token.
const provimuxSessionRouteBodyLimit = 64 << 10

// registerProvimuxClientRoutes adds the two paths to the authenticated /v1 group, so they pass its client-key
// middleware like every model request, and starts the book of the server's manager.
func (s *Server) registerProvimuxClientRoutes(v1 *gin.RouterGroup) {
	if s == nil || v1 == nil {
		return
	}
	if s.handlers != nil {
		sessionroute.Start(s.handlers.AuthManager)
	}
	v1.POST("/provimux/session-route", s.provimuxShowSessionRoute)
	v1.POST("/provimux/session-route/release", s.provimuxReleaseSessionRoute)
}

// provimuxSessionRouteRequest is ShowRequest and ReleaseRequest; expect is the release's only.
type provimuxSessionRouteRequest struct {
	Model    string          `json:"model"`
	Identity json.RawMessage `json:"identity,omitempty"`
	Expect   *string         `json:"expect,omitempty"`
}

// provimuxSessionRouteError answers in the /v1 group's error shape.
func provimuxSessionRouteError(c *gin.Context, status int, message, code string) {
	errorBody := gin.H{"message": message, "type": "invalid_request_error"}
	if code != "" {
		errorBody["code"] = code
	}
	c.JSON(status, gin.H{"error": errorBody})
}

// provimuxSessionRouteParse reads the body as the contract's schema: an object with a non-empty model, identity an
// object where given, expect a string and only on release, and nothing else.
func (s *Server) provimuxSessionRouteParse(c *gin.Context, release bool) (provimuxSessionRouteRequest, bool) {
	var request provimuxSessionRouteRequest
	raw, errRead := io.ReadAll(io.LimitReader(c.Request.Body, provimuxSessionRouteBodyLimit+1))
	if errRead != nil || len(raw) > provimuxSessionRouteBodyLimit {
		provimuxSessionRouteError(c, http.StatusBadRequest, "the body could not be read, or is larger than 64 KiB", "")
		return request, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(&request); errDecode != nil || decoder.More() {
		provimuxSessionRouteError(c, http.StatusBadRequest, "the body must be a JSON object with model (and identity, or expect on release) only", "")
		return request, false
	}
	if !release && request.Expect != nil {
		provimuxSessionRouteError(c, http.StatusBadRequest, "expect belongs to the release request", "")
		return request, false
	}
	request.Model = strings.TrimSpace(request.Model)
	if request.Model == "" {
		provimuxSessionRouteError(c, http.StatusBadRequest, "model is required", "")
		return request, false
	}
	if identity := bytes.TrimSpace(request.Identity); len(identity) > 0 && !bytes.Equal(identity, []byte("null")) {
		if identity[0] != '{' {
			provimuxSessionRouteError(c, http.StatusBadRequest, "identity must be an object", "")
			return request, false
		}
		request.Identity = identity
	} else {
		request.Identity = nil
	}
	if rule := onlymynames.Current(); rule.Applies() && rule.Refuses(request.Model) {
		onlymynames.Count(rule, request.Model)
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": onlymynames.NotServedMessage(request.Model), "type": "invalid_request_error", "code": "model_not_found", "param": "model"}})
		return request, false
	}
	return request, true
}

// provimuxSessionRouteScope is the caller scope upstream computes for this request's client key (an irreversible hash),
// or "" when no client key authenticated it: such a caller is seen using nothing.
func provimuxSessionRouteScope(c *gin.Context) string {
	value, exists := c.Get("userApiKey")
	if !exists || value == nil {
		return ""
	}
	return coresession.CallerScope(fmt.Sprint(value))
}

func (s *Server) provimuxSessionBook() *coreauth.ProvimuxSessionBook {
	if s == nil || s.handlers == nil {
		return sessionroute.Book(nil)
	}
	return sessionroute.Book(s.handlers.AuthManager)
}

// provimuxShowSessionRoute is POST /v1/provimux/session-route. It changes nothing.
func (s *Server) provimuxShowSessionRoute(c *gin.Context) {
	request, ok := s.provimuxSessionRouteParse(c, false)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, s.provimuxSessionBook().Show(provimuxSessionRouteScope(c), c.Request.Header, request.Identity, request.Model))
}

// provimuxReleaseSessionRoute is POST /v1/provimux/session-route/release.
func (s *Server) provimuxReleaseSessionRoute(c *gin.Context) {
	request, ok := s.provimuxSessionRouteParse(c, true)
	if !ok {
		return
	}
	expect := ""
	if request.Expect != nil {
		expect = *request.Expect
	}
	released, conflict := s.provimuxSessionBook().Release(provimuxSessionRouteScope(c), c.Request.Header, request.Identity, request.Model, expect)
	if conflict != nil {
		c.JSON(http.StatusConflict, conflict)
		return
	}
	c.JSON(http.StatusOK, released)
}
