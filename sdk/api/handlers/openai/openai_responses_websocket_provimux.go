package openai

import (
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

// Provimux: who answered, on the Responses WebSocket (.github/provimux/design/answered-by.md 5.2). Each turn's
// response.created carries the provimux_* fields beside cpa_auth_kind, cpa_auth_provider and cpa_auth_host, from the
// one statement the turn's sink received. With models.answered-by off, response.created is left as the owner's patch
// writes it.

// removeResponsesWebsocketProvimux removes from a response.created event every provimux_* field of the statement an
// upstream may have put there (an upstream that is itself a Provimux names its own account): they are this gateway's
// own, and each is absent when this gateway's statement has no value for it.
func removeResponsesWebsocketProvimux(payload []byte) []byte {
	if !logging.ProvimuxAnsweredBy() {
		return payload
	}
	for _, name := range logging.ProvimuxFieldNames() {
		if !ojson.GetBytesNoCopy(payload, name.Field).Exists() {
			continue
		}
		if removed, errDelete := ojson.DeleteBytes(payload, name.Field); errDelete == nil {
			payload = removed
		}
	}
	return payload
}

// setResponsesWebsocketProvimux puts on a response.created event the provimux_* fields of the turn's statement, as
// every transport sends them (logging.AuthStatement.OnTheWire), only when the event carries that statement's
// cpa_auth_* values: the fields are never written apart from the statement they belong to.
func setResponsesWebsocketProvimux(payload []byte, authStatement func() logging.AuthStatement) []byte {
	if authStatement == nil || !logging.ProvimuxAnsweredBy() {
		return payload
	}
	statement := authStatement().OnTheWire()
	if statement.Kind == "" {
		return payload
	}
	for _, field := range []struct{ name, value string }{
		{responsesWebsocketAuthKindField, statement.Kind},
		{responsesWebsocketAuthProviderField, statement.Provider},
		{responsesWebsocketAuthHostField, statement.Host},
	} {
		value := ojson.GetBytesNoCopy(payload, field.name)
		if (field.value == "") == value.Exists() || (field.value != "" && value.String() != field.value) {
			return payload
		}
	}
	for _, field := range statement.Provimux.Fields() {
		if named, errSet := ojson.SetBytes(payload, field.Field, field.Value); errSet == nil {
			payload = named
		}
	}
	return payload
}
