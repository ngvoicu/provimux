package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/onlymynames"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/sessionroute"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
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

// provimuxSessionRouteRequest is ShowRequest or ReleaseRequest, read strictly.
type provimuxSessionRouteRequest struct {
	Model    string
	Identity []byte // the identity object as sent, or nil
	Expect   string // "" when absent
}

// provimuxSessionRouteError answers in the /v1 group's error shape.
func provimuxSessionRouteError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": gin.H{"message": message, "type": "invalid_request_error"}})
}

// provimuxParseSessionRouteBody reads the body as the contract's schema, and nothing looser: one JSON object, nothing
// after it; each field at most once; model a non-empty string; identity an object where present; expect, on the
// release only, a well-formed binding token where present (an empty, blank or null expect is refused, never taken as
// absent); no other field.
func provimuxParseSessionRouteBody(raw []byte, release bool) (provimuxSessionRouteRequest, error) {
	var request provimuxSessionRouteRequest
	notObject := errors.New("the body must be one JSON object, with nothing after it")
	decoder := json.NewDecoder(bytes.NewReader(raw))
	open, errOpen := decoder.Token()
	if errOpen != nil || open != json.Delim('{') {
		return request, notObject
	}
	seen := map[string]bool{}
	for decoder.More() {
		keyToken, errKey := decoder.Token()
		if errKey != nil {
			return request, notObject
		}
		key, _ := keyToken.(string)
		if seen[key] {
			return request, fmt.Errorf("%s is given twice", key)
		}
		seen[key] = true
		var value json.RawMessage
		if errValue := decoder.Decode(&value); errValue != nil {
			return request, notObject
		}
		trimmed := bytes.TrimSpace(value)
		isNull := bytes.Equal(trimmed, []byte("null"))
		switch {
		case key == "model":
			if isNull || json.Unmarshal(trimmed, &request.Model) != nil {
				return request, errors.New("model must be a string")
			}
		case key == "identity":
			if len(trimmed) == 0 || trimmed[0] != '{' {
				return request, errors.New("identity must be an object")
			}
			request.Identity = append([]byte(nil), trimmed...)
		case key == "expect" && release:
			if isNull || json.Unmarshal(trimmed, &request.Expect) != nil || !coreauth.ProvimuxValidBindingToken(request.Expect) {
				return request, errors.New("expect must be the binding token a show gave (b. and 16 characters); leave it out to release whatever is bound")
			}
		case key == "expect":
			return request, errors.New("expect belongs to the release request")
		default:
			return request, fmt.Errorf("unknown field %s", key)
		}
	}
	if closing, errClose := decoder.Token(); errClose != nil || closing != json.Delim('}') {
		return request, notObject
	}
	if _, errAfter := decoder.Token(); !errors.Is(errAfter, io.EOF) {
		return request, notObject
	}
	request.Model = strings.TrimSpace(request.Model)
	if request.Model == "" {
		return request, errors.New("model is required")
	}
	return request, nil
}

// provimuxRoutedName resolves the name the client sent as the request path does before it chooses an account: with
// "serve only my names" in force, auto becomes the first of his served names (the door); then, outside Home mode,
// upstream resolves auto to the first available model. The thinking suffix is kept, as there. Any other name is routed
// as it is. A plugin's model router, which reads the turn's body, is not reproduced: the name it routed to is the one
// to ask about. It returns the resolved name and the name the door judges.
func (s *Server) provimuxRoutedName(model string) (routed, judged string) {
	name := strings.TrimSpace(model)
	if rule := onlymynames.Current(); rule.Applies() {
		if parsed := thinking.ParseSuffix(name); strings.TrimSpace(parsed.ModelName) == onlymynames.Auto {
			if first := registry.ProvimuxFirstServedUnder(rule); first != "" {
				name = first
				if parsed.HasSuffix {
					name = fmt.Sprintf("%s(%s)", first, parsed.RawSuffix)
				}
			}
		}
	}
	judged = name
	if s != nil && s.handlers != nil && s.handlers.AuthManager != nil && s.handlers.AuthManager.HomeEnabled() {
		return name, judged
	}
	if parsed := thinking.ParseSuffix(name); parsed.ModelName == "auto" {
		resolved := util.ResolveAutoModel(parsed.ModelName)
		if parsed.HasSuffix {
			resolved = fmt.Sprintf("%s(%s)", resolved, parsed.RawSuffix)
		}
		return resolved, judged
	}
	return util.ResolveAutoModel(name), judged
}

// provimuxSessionRouteParse reads the body and resolves the name; on any refusal it has answered 400.
func (s *Server) provimuxSessionRouteParse(c *gin.Context, release bool) (request provimuxSessionRouteRequest, routed string, ok bool) {
	raw, errRead := io.ReadAll(io.LimitReader(c.Request.Body, provimuxSessionRouteBodyLimit+1))
	if errRead != nil || len(raw) > provimuxSessionRouteBodyLimit {
		provimuxSessionRouteError(c, http.StatusBadRequest, "the body could not be read, or is larger than 64 KiB")
		return request, "", false
	}
	request, errParse := provimuxParseSessionRouteBody(raw, release)
	if errParse != nil {
		provimuxSessionRouteError(c, http.StatusBadRequest, errParse.Error())
		return request, "", false
	}
	routed, judged := s.provimuxRoutedName(request.Model)
	if rule := onlymynames.Current(); rule.Applies() && rule.Refuses(judged) {
		onlymynames.Count(rule, judged)
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": onlymynames.NotServedMessage(judged), "type": "invalid_request_error", "code": "model_not_found", "param": "model"}})
		return request, "", false
	}
	return request, routed, true
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
	request, routed, ok := s.provimuxSessionRouteParse(c, false)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, s.provimuxSessionBook().ShowRouted(provimuxSessionRouteScope(c), c.Request.Header, request.Identity, request.Model, routed))
}

// provimuxReleaseSessionRoute is POST /v1/provimux/session-route/release.
func (s *Server) provimuxReleaseSessionRoute(c *gin.Context) {
	request, routed, ok := s.provimuxSessionRouteParse(c, true)
	if !ok {
		return
	}
	released, conflict := s.provimuxSessionBook().ReleaseRouted(provimuxSessionRouteScope(c), c.Request.Header, request.Identity, request.Model, routed, request.Expect)
	if conflict != nil {
		c.JSON(http.StatusConflict, conflict)
		return
	}
	c.JSON(http.StatusOK, released)
}
