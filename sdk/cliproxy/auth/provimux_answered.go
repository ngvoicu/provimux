package auth

import (
	"context"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Provimux: the whole statement of who answered (.github/provimux/design/answered-by.md 5). The conductor states who
// answered once per call (publishAnsweredAuthMetadata) through cliproxyexecutor.AnsweredAuthCallbackMetadataKey,
// which takes the statement's first three values. A caller that also installs ProvimuxAnswerCallbackMetadataKey
// receives the same statement whole, Provimux's values included, in the same call and right after.

// ProvimuxAnswerCallbackMetadataKey carries an optional func(logging.AuthStatement): the whole statement of the
// response a call returns, or the zero statement where nobody is named. It is called only where
// AnsweredAuthCallbackMetadataKey's callback is, and right after it.
const ProvimuxAnswerCallbackMetadataKey = "provimux_answer_callback"

// provimuxAnswerEntry holds the one function that gives an answer's entry: steps 3 to 5 of answered-by.md 4.3, for the
// credential that answered and what its attempt sent. sdk/cliproxy sets it (SetProvimuxAnswerEntry); nil = no entry.
var provimuxAnswerEntry atomic.Pointer[func(auth *Auth, sent cliproxyexecutor.ProvimuxSentRequest) string]

// SetProvimuxAnswerEntry sets the function that gives an answer's entry (answered-by.md 4.3, steps 3 to 5); nil
// removes it, and no answer names an entry.
func SetProvimuxAnswerEntry(entry func(auth *Auth, sent cliproxyexecutor.ProvimuxSentRequest) string) {
	if entry == nil {
		provimuxAnswerEntry.Store(nil)
		return
	}
	provimuxAnswerEntry.Store(&entry)
}

// publishProvimuxAnswer completes the statement the conductor has just made for the response it returns, and hands it
// to ProvimuxAnswerCallbackMetadataKey. It is called only from publishAnsweredAuthMetadata, after its callback, with
// the same attempt context, credential and statement; for an empty statement it hands over the empty statement.
//
// A statement that names an account gains what its attempt sent (answered-by.md 4.3): where the attempt's
// observations agree on one model request that was answered (cliproxyexecutor.ProvimuxSent), its model, and the
// entry the gateway gives it for this credential. Where they do not, neither: never a value from anywhere else.
func publishProvimuxAnswer(attemptCtx context.Context, meta map[string]any, auth *Auth, statement logging.AuthStatement) {
	callback, ok := meta[ProvimuxAnswerCallbackMetadataKey].(func(logging.AuthStatement))
	if !ok || callback == nil {
		return
	}
	if statement.Provimux.Account != "" && auth != nil {
		if sent, okSent := cliproxyexecutor.ProvimuxSent(attemptCtx); okSent {
			statement.Provimux.ModelSent = sent.Model
			if entry := provimuxAnswerEntry.Load(); entry != nil && *entry != nil {
				statement.Provimux.Entry = (*entry)(auth, sent)
			}
		}
	}
	callback(statement)
}
