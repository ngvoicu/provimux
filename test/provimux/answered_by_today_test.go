package provimux

import "testing"

// Provimux: with models.answered-by off, the account lists and the answers are byte for byte what the commit
// before answered-by writes for the same accounts and requests (answered-by.md 6 and 8.4), though the markers' key
// is set: every header (an upstream gateway's X-Provimux-* among them), every body, every SSE frame and every
// WebSocket event; and GET /v1/models without include is that commit's whatever the switch. The golden files are
// that commit's output (see answered_by_fixture_test.go).
func TestProvimuxAnsweredByOffIsToday(t *testing.T) {
	manager, _ := registerAnsweredByLists(t)

	answeredBySwitch(t, false)
	checkAnsweredGolden(t, "models.json", answeredByModels(t, manager, ""))
	checkAnsweredGolden(t, "models-accounts.json", answeredByModels(t, manager, "?include=accounts"))
	checkAnsweredGolden(t, "model-routes.json", answeredByRoutes(t, manager))
	answeredBySwitch(t, true)
	checkAnsweredGolden(t, "models.json", answeredByModels(t, manager, ""))

	answeredBySwitch(t, false)
	checkAnsweredGolden(t, "answers.txt", answeredByAnswers(t))
}
