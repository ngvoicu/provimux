package auth

import (
	"context"
	"sync/atomic"
	"time"

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

// ProvimuxPluginCredentialMark is the metadata key that marks a credential a plugin changed with the models it
// returned (the service sets it where it commits the update, before the update is stored). It is stored with the
// credential, so it outlives the process: a login file a plugin rewrote is read at the next start by the native
// loader, whose credential has no plugin storage. A native login that writes the file anew drops it.
const ProvimuxPluginCredentialMark = "provimux_plugin_credential"

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

// ProvimuxAnsweredItem is one item of what an account's answers for a name said of themselves (answered-by.md 4.4):
// an entry, "" for answers that named none, and when the last answer that said so was stated.
type ProvimuxAnsweredItem struct {
	Entry string
	At    time.Time
}

// provimuxAnsweredRecord holds the record of answered-by.md 4.4, set by sdk/cliproxy (SetProvimuxAnswered): note is
// called for every statement that names an account, with its marker, the credential, the model the client asked
// for and the statement's entry; read gives a marker's items under a name, newest first. nil = nothing recorded.
var provimuxAnsweredRecord atomic.Pointer[provimuxAnsweredHooks]

type provimuxAnsweredHooks struct {
	note func(marker string, auth *Auth, requestedModel, entry string)
	read func(marker, model string) []ProvimuxAnsweredItem
}

// SetProvimuxAnswered sets the record's two functions; nil for either removes both.
func SetProvimuxAnswered(note func(marker string, auth *Auth, requestedModel, entry string), read func(marker, model string) []ProvimuxAnsweredItem) {
	if note == nil || read == nil {
		provimuxAnsweredRecord.Store(nil)
		return
	}
	provimuxAnsweredRecord.Store(&provimuxAnsweredHooks{note: note, read: read})
}

// ProvimuxAnswered returns what the answers of the account with marker said for model, newest first; nil without a
// marker, with models.answered-by off, or without a record. Both lists read it.
func ProvimuxAnswered(marker, model string) []ProvimuxAnsweredItem {
	hooks := provimuxAnsweredRecord.Load()
	if hooks == nil || marker == "" || model == "" || !logging.ProvimuxAnsweredBy() {
		return nil
	}
	return hooks.read(marker, model)
}

// publishProvimuxAnswer completes the statement the conductor has just made for the response it returns, and hands it
// to ProvimuxAnswerCallbackMetadataKey. It is called only from publishAnsweredAuthMetadata, after its callback, with
// the same attempt context, credential and statement; for an empty statement it hands over the empty statement.
//
// A statement that names an account gains what its attempt sent (answered-by.md 4.3): where the attempt's
// observations agree on one model request that was answered (cliproxyexecutor.ProvimuxSent), its model, and the
// entry the gateway gives it for this credential. Where they do not, neither: never a value from anywhere else. It is
// then noted in the record of what the account's answers said (answered-by.md 4.4), whatever the entry, none included.
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
		// The record (answered-by.md 4.4), before the statement is handed on: so before the answer's first byte.
		if hooks := provimuxAnsweredRecord.Load(); hooks != nil {
			hooks.note(statement.Provimux.Account, auth, stringMetadataValue(meta, cliproxyexecutor.RequestedModelMetadataKey), statement.Provimux.Entry)
		}
	}
	callback(statement)
}
