package management

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: a route's `answered` behind the management key (answered-by.md 4.4): the time in UTC to the millisecond,
// null for none, empty when nothing is recorded, left out without a marker; and the entries routes name, the
// selection of figures.entries. Invented values.
func TestProvimuxAnsweredTimes(t *testing.T) {
	was := logging.SetProvimuxAnsweredBy(true)
	t.Cleanup(func() { logging.SetProvimuxAnsweredBy(was); coreauth.SetProvimuxAnswered(nil, nil) })
	at := time.Date(2030, 1, 2, 23, 59, 59, 123456789, time.FixedZone("provimux-test", 3600)) // date-safe: a fixed value of a pure function
	coreauth.SetProvimuxAnswered(func(string, *coreauth.Auth, string, string) {}, func(marker, model string) []coreauth.ProvimuxAnsweredItem {
		if marker == "a.aaaaaaaaaaaaaaaaaaaa" && model == "m" {
			return []coreauth.ProvimuxAnsweredItem{{Entry: "p/one", At: at}, {Entry: "", At: at.Add(-time.Hour)}}
		}
		return nil
	})
	marker, other := "a.aaaaaaaaaaaaaaaaaaaa", "a.bbbbbbbbbbbbbbbbbbbb"
	if got := provimuxAnsweredTimes(nil, "m"); got != nil {
		t.Fatalf("without a marker: %v", got)
	}
	if got := provimuxAnsweredTimes(&other, "m"); got == nil || len(*got) != 0 {
		t.Fatalf("nothing recorded: %v", got)
	}
	got := provimuxAnsweredTimes(&marker, "m")
	if got == nil || len(*got) != 2 || *(*got)[0].Entry != "p/one" || (*got)[0].At != "2030-01-02T22:59:59.123Z" || (*got)[1].Entry != nil { // date-safe: the fixed value above, in UTC
		t.Fatalf("items: %+v", got)
	}
	entries := provimuxAnsweredEntries([]provimuxModel{{Routes: []provimuxRoute{{Answered: got}, {}}}})
	if len(entries) != 1 || !entries["p/one"] {
		t.Fatalf("the entries routes name: %v", entries)
	}
}
