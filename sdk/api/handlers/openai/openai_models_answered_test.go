package openai

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: a row's `answered` in the client list (answered-by.md 4.4 as amended on 2026-11-27): the caller's own
// record only, the day only, null for none, empty when nothing is recorded (another key's record included), left out
// without a marker; and the entries the rows name, for include=figures. Invented values.
func TestProvimuxAnsweredDays(t *testing.T) {
	was := logging.SetProvimuxAnsweredBy(true)
	t.Cleanup(func() { logging.SetProvimuxAnsweredBy(was); coreauth.SetProvimuxAnswered(nil, nil) })
	at := time.Date(2030, 1, 2, 23, 59, 59, 0, time.FixedZone("provimux-test", 3600)) // date-safe: a fixed value of a pure function
	coreauth.SetProvimuxAnswered(func(string, string) string { return "" }, func(client string, all bool, authID, marker, model string) []coreauth.ProvimuxAnsweredItem {
		if marker == "a.aaaaaaaaaaaaaaaaaaaa" && model == "m" && authID == "cred" && (all || client == "k.client") {
			return []coreauth.ProvimuxAnsweredItem{{Entry: "p/one", At: at}, {Entry: "", At: at.Add(-time.Hour)}}
		}
		return nil
	})
	if got := provimuxAnsweredDays("k.client", "cred", "", "m"); got != nil {
		t.Fatalf("without a marker: %v", got)
	}
	if got := provimuxAnsweredDays("k.other", "cred", "a.aaaaaaaaaaaaaaaaaaaa", "m"); got == nil || len(*got) != 0 {
		t.Fatalf("nothing recorded: %v", got)
	}
	got := provimuxAnsweredDays("k.client", "cred", "a.aaaaaaaaaaaaaaaaaaaa", "m")
	if got == nil || len(*got) != 2 || (*got)[0].Entry == nil || *(*got)[0].Entry != "p/one" || (*got)[0].On != "2030-01-02" || (*got)[1].Entry != nil { // date-safe: the fixed value above, in UTC
		t.Fatalf("items: %+v", got)
	}
	entries := answeredEntries(map[string][]modelAccount{"m": {{Answered: got}, {}}})
	if len(entries) != 1 || !entries["p/one"] {
		t.Fatalf("the entries rows name: %v", entries)
	}
}
