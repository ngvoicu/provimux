package provimux

import "testing"

// Provimux: a caller that does not use the session-route paths sees no change (session-route.md 12 row 1; G1): with
// session-route's book started and observing every answer, every existing path writes byte for byte what the commit
// before session-route writes for the same accounts, keys and requests (every status, header and body), and every
// turn goes to the same account. The golden file is that commit's output (see session_route_fixture_test.go).
func TestProvimuxSessionRouteLeavesOtherPathsAsToday(t *testing.T) {
	sessionRouteStart(t)
	answers, manager := sessionRouteFixtureAnswers(t)
	checkSessionRouteGolden(t, "answers.txt", answers)
	sessionRouteObserved(t, manager)
}
