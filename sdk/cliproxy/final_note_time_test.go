package cliproxy

import (
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	ca "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestFinalLastWrittenTime(t *testing.T) {
	h := newSentHarness(t, reviewRouter)
	c := newSentClock()
	// A correction of two seconds crosses UTC midnight between two completed answers.
	first := c.Now().Truncate(24 * time.Hour).Add(time.Second)
	c.add(first.Sub(c.Now()))
	b := useAnsweredBook(t, c)
	a := sentKeyFor(t, h, "review-public")
	for i := 0; i < 2; i++ {
		result := h.post("/v1/chat/completions", chatBody("review-public", false, ""), nil)
		if result.status != 200 || result.get(logging.ProvimuxEntryHeader) != "openrouter/lab/native" {
			t.Fatal("fixture", result.status)
		}
		if i == 0 {
			c.add(-2 * time.Second)
		}
	}
	last := c.Now()
	got := b.read(ca.ProvimuxClientRef(sentClientKey), false, ca.ProvimuxCredentialRef(a.ID), a.ProvimuxAccountMarker(), "review-public")
	mine, all, _ := h.listsRow(sentClientKey, "review-public", a.ProvimuxAccountMarker())
	if len(got) != 1 || mine == nil || all == nil {
		t.Fatal("missing row")
	}
	t.Logf("first=%s latest=%s, keyed list=%v management=%v", first.Format(time.RFC3339), last.Format(time.RFC3339), mine.items(), all.items())
	// date-safe: a layout string, not a date; the times come from the test's own clock
	if !got[0].At.Equal(last) || !strings.HasSuffix(mine.items()[0], last.Format("2006-01-02")) {
		t.Error("the time and UTC day shown belong to an earlier answer")
	}
}
