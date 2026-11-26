package auth

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

// Provimux: the whole statement of who answered (.github/provimux/design/answered-by.md 5). The conductor states who
// answered once per call (publishAnsweredAuthMetadata) through cliproxyexecutor.AnsweredAuthCallbackMetadataKey,
// which takes the statement's first three values. A caller that also installs ProvimuxAnswerCallbackMetadataKey
// receives the same statement whole, Provimux's values included, in the same call and right after.

// ProvimuxAnswerCallbackMetadataKey carries an optional func(logging.AuthStatement): the whole statement of the
// response a call returns, or the zero statement where nobody is named. It is called only where
// AnsweredAuthCallbackMetadataKey's callback is, and right after it.
const ProvimuxAnswerCallbackMetadataKey = "provimux_answer_callback"

// publishProvimuxAnswer hands the statement the conductor has just made for the response it returns to
// ProvimuxAnswerCallbackMetadataKey. It is called only from publishAnsweredAuthMetadata, after its callback, with the
// same attempt context, credential and statement; for an empty statement it hands over the empty statement.
func publishProvimuxAnswer(_ context.Context, meta map[string]any, _ *Auth, statement logging.AuthStatement) {
	if callback, ok := meta[ProvimuxAnswerCallbackMetadataKey].(func(logging.AuthStatement)); ok && callback != nil {
		callback(statement)
	}
}
