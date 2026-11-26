package handlers

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"golang.org/x/net/context"
)

// Provimux: the whole statement of who answered (.github/provimux/design/answered-by.md 5): the three X-CPA-AUTH-*
// values and Provimux's, captured, offered and written as one value.

// setStatement is the capture's second setter: the conductor hands it the whole statement of the call's result through
// coreauth.ProvimuxAnswerCallbackMetadataKey, right after set has received its first three values.
func (a *answeredAuthCapture) setStatement(statement logging.AuthStatement) {
	a.mu.Lock()
	a.statement = statement
	a.mu.Unlock()
}

type answeredStatementCallbackContextKey struct{}

// WithAnsweredStatementCallback installs a callback that receives the whole statement of the result the execution
// layer returns, where WithAnsweredAuthCallback receives its first three values; when both are installed only this one
// is called. A websocket turn uses it in place of response headers.
func WithAnsweredStatementCallback(ctx context.Context, callback func(logging.AuthStatement)) context.Context {
	if callback == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, answeredStatementCallbackContextKey{}, callback)
}

func answeredStatementCallbackFromContext(ctx context.Context) func(logging.AuthStatement) {
	if ctx == nil {
		return nil
	}
	callback, _ := ctx.Value(answeredStatementCallbackContextKey{}).(func(logging.AuthStatement))
	return callback
}
