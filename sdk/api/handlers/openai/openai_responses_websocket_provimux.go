package openai

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: who answered, on the Responses WebSocket (.github/provimux/design/answered-by.md 5.2). Each turn's
// response.created carries the provimux_* fields beside cpa_auth_kind, cpa_auth_provider and cpa_auth_host, from the
// one statement the turn's sink received. The provimux_* fields an upstream put there are removed whatever the switch;
// with models.answered-by off none is written.

// removeResponsesWebsocketProvimux removes from a response.created event every provimux_* field of the statement an
// upstream may have put there (an upstream that is itself a Provimux names its own account): they are this gateway's
// own, and each is absent when this gateway's statement has no value for it. Switch on or off: a marker that is not
// ours never reaches a client.
func removeResponsesWebsocketProvimux(payload []byte) []byte {
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

// provimuxNoteResponsesWebsocketTurn hands the record the turn's statement once its response.created, carrying that
// statement's marker, has been written to the client (answered-by.md 4.4 as amended on 2026-11-27), on a connection
// the stock output route accepted (the amendment of 2026-11-27 (2), rule B.1).
func provimuxNoteResponsesWebsocketTurn(c *gin.Context, eventType string, payload []byte, authStatement func() logging.AuthStatement) {
	if c == nil || eventType != "response.created" || authStatement == nil || !logging.ProvimuxAnsweredBy() {
		return
	}
	statement := authStatement()
	if account := statement.OnTheWire().Provimux.Account; account == "" || ojson.GetBytesNoCopy(payload, logging.ProvimuxAccountField).String() != account {
		return
	}
	logging.ProvimuxNoteWritten(c.Request, statement)
}

// provimuxCalledName says on a call's context the name its client called, where the route passes the manager another
// (a speech call): the record of what answers said stands under the name called.
func provimuxCalledName(ctx context.Context, name string) context.Context {
	return coreauth.WithProvimuxCalledName(ctx, name)
}
