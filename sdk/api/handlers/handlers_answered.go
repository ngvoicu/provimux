package handlers

import (
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"golang.org/x/net/context"
)

// answeredAuthCapture receives the conductor's statement for one execution call: the conductor makes it once, where
// the call returns a response, through coreexecutor.AnsweredAuthCallbackMetadataKey. It belongs to that call alone, so
// another execution on the same request (a plugin's own model call, a route's second call) cannot replace it.
type answeredAuthCapture struct {
	mu        sync.Mutex
	statement logging.AuthStatement
}

func (a *answeredAuthCapture) set(kind, provider, host string) {
	a.mu.Lock()
	a.statement = logging.AuthStatement{Kind: kind, Provider: provider, Host: host}
	a.mu.Unlock()
}

func (a *answeredAuthCapture) get() logging.AuthStatement {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.statement
}

// offerAnsweredAuth hands the statement of the result being returned to whoever writes it: a websocket turn's sink,
// or the request's response state, where it names nothing until the route confirms it (ConfirmAnswer). Every return
// of an execution offers: the empty statement for an error or a result no upstream produced.
func offerAnsweredAuth(ctx context.Context, internalSource bool, s logging.AuthStatement) {
	if ctx == nil { // nobody to offer to; the execution returns its result or its error as it is
		return
	}
	if internalSource { // a plugin's own model call: it answers the plugin, not the client
		return
	}
	if sink := answeredAuthCallbackFromContext(ctx); sink != nil { // a websocket turn
		sink(s.Kind, s.Provider, s.Host)
		return
	}
	if g, ok := ctx.Value("gin").(*gin.Context); ok && g != nil && g.Request != nil && !websocket.IsWebSocketUpgrade(g.Request) {
		logging.OfferGinCPAAuthStatement(g, s)
	}
}

// ConfirmAnswer is called by a route immediately before it writes the first byte of the result the execution layer
// last returned on this request, with nothing between that can write. The headers that write commits then name the
// credential whose upstream produced that result (X-CPA-AUTH-KIND, X-CPA-AUTH-PROVIDER, X-CPA-AUTH-HOST); every other
// commit carries none of them. A custom route built on BaseAPIHandler's Execute…WithAuthManager functions gets none
// of the three headers until it calls ConfirmAnswer. After the headers are committed the call has no effect.
func ConfirmAnswer(c *gin.Context) {
	if c == nil || c.Writer.Written() {
		return
	}
	logging.ConfirmGinCPAAuthOffer(c)
}
