package cliproxy

import (
	"reflect"
	"testing"
)

func TestFinalTiesOnReopen(t *testing.T) {
	c := newSentClock()
	dir := t.TempDir()
	b := newProvimuxAnsweredBook(c.Now)
	b.setPlace(dir)
	for _, e := range []string{"p/1", "p/2", "p/3", "p/4", "p/5"} {
		b.note("A", "c", "m", "n", e)
	}
	b.note("B", "c", "m", "n", "p/6")
	check := func(book *provimuxAnsweredBook) {
		for _, tc := range []struct {
			client string
			all    bool
			want   []string
		}{{"A", false, []string{"p/5", "p/4", "p/3", "p/2"}}, {"B", false, []string{"p/6"}}, {"", true, []string{"p/6", "p/5", "p/4", "p/3"}}} {
			var got []string
			for _, item := range book.read(tc.client, tc.all, "c", "m", "n") {
				got = append(got, item.Entry)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("client=%q all=%v got=%v", tc.client, tc.all, got)
			}
		}
	}
	check(b)
	b.close()
	reopened := newProvimuxAnsweredBook(c.Now)
	reopened.setPlace(dir)
	defer reopened.close()
	check(reopened)
}
