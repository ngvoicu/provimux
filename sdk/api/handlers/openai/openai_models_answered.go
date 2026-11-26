package openai

import (
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: `answered` on an account row of GET /v1/models?include=accounts (.github/provimux/design/answered-by.md
// 4.4; prices-and-cache/openapi.yaml, Answered). The record is of every client's answers, so a client key's holder
// gets the day of each item, never the time: a time would tell him when another key's holder was at work.

// modelAnswered is one item: the entry those answers named (null: none) and the day, in UTC, of the last of them.
type modelAnswered struct {
	Entry *string `json:"entry"`
	On    string  `json:"on"`
}

// provimuxAnsweredDays is the row's `answered` for the account with marker under model: empty when nothing is
// recorded, nil (left out) without a marker.
func provimuxAnsweredDays(marker, model string) *[]modelAnswered {
	if marker == "" {
		return nil
	}
	items := coreauth.ProvimuxAnswered(marker, model)
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
// include=figures (prices-and-cache.md 5), from the same rows the list gives.
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
