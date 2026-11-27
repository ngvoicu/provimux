package openai

import (
	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: `answered` on an account row of GET /v1/models?include=accounts (.github/provimux/design/answered-by.md
// 4.4 as amended on 2026-11-27; prices-and-cache/openapi.yaml, Answered). A row gives what this credential's answers
// for this model to the caller's own key said, by day: one client cannot watch another's through the list. Callers
// without a key share one record.

// modelAnswered is one item: the entry those answers named (null: none) and the day, in UTC, of the last of them.
type modelAnswered struct {
	Entry *string `json:"entry"`
	On    string  `json:"on"`
}

// provimuxFillAnswered gives every row that has a marker its `answered` for the caller of c.
func provimuxFillAnswered(c *gin.Context, accounts map[string][]modelAccount) {
	client := provimuxCallerRef(c)
	for model, rows := range accounts {
		for i := range rows {
			rows[i].Answered = provimuxAnsweredDays(client, rows[i].credential, rows[i].Account, model)
		}
	}
}

// provimuxCallerRef is the record's reference of the key the request was authenticated with; "" without one.
func provimuxCallerRef(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if key, exists := c.Get("userApiKey"); exists {
		if text, ok := key.(string); ok {
			return coreauth.ProvimuxClientRef(text)
		}
	}
	return ""
}

// provimuxAnsweredDays is the row's `answered`: empty when nothing is recorded, nil (left out) without a marker or
// without a record (off, or a process Provimux's own main did not start).
func provimuxAnsweredDays(client, credential, marker, model string) *[]modelAnswered {
	if marker == "" || !coreauth.ProvimuxRecordOn() {
		return nil
	}
	items := coreauth.ProvimuxAnswered(client, credential, marker, model)
	out := make([]modelAnswered, 0, len(items))
	for _, item := range items {
		answered := modelAnswered{On: item.At.UTC().Format("2006-01-02")}
		if item.Entry != "" {
			entry := item.Entry
			answered.Entry = &entry
		}
		out = append(out, answered)
	}
	return &out
}

// answeredEntries is the set of entries the rows' `answered` items name: the selection of figures.entries for
// include=figures (prices-and-cache.md 5), from the same rows the list gives this caller.
func answeredEntries(accounts map[string][]modelAccount) map[string]bool {
	entries := map[string]bool{}
	for _, rows := range accounts {
		for _, row := range rows {
			if row.Answered == nil {
				continue
			}
			for _, item := range *row.Answered {
				if item.Entry != nil {
					entries[*item.Entry] = true
				}
			}
		}
	}
	return entries
}
