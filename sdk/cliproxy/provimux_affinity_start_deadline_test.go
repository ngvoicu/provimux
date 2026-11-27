package cliproxy

import (
	"strings"
	"testing"
	"time"
)

// Note 8: the start's disk work has a deadline like the stop's write. On a disk that does not answer (here: the
// start's read held until the test lets it go), the start goes on without restoring and the state says why; the read
// that returns later is not used, and the writer works from then on.
func TestProvimuxAffinityStartDeadline(t *testing.T) {
	dir := t.TempDir()
	a, b := pmxClaude("provimux-test-a.json", "acc-a"), pmxClaude("provimux-test-b.json", "acc-b")
	earlier := pmxService(t, dir, "", nil, a, b)
	pmxStart(t, earlier)
	pmxBind(t, earlier, "provimux-test-session", a, b)
	pmxWrite(earlier)
	pmxEnd(earlier)

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	hook := func() {
		entered <- struct{}{}
		<-release
	}
	provimuxAffinityBeforeRead.Store(&hook)
	defer provimuxAffinityBeforeRead.Store(nil)
	previous := provimuxAffinityStartDeadline
	provimuxAffinityStartDeadline = 20 * time.Millisecond // the read is held until released: it always passes first
	defer func() { provimuxAffinityStartDeadline = previous }()

	s := pmxService(t, dir, "", nil, a, b)
	status := pmxStart(t, s) // returns while the read is held
	<-entered
	if status.RestoredAtStart != nil || !strings.Contains(status.StartNote, "did not answer within") {
		t.Fatalf("state while the disk does not answer: %+v", status)
	}
	if _, state := pmxBound(s, "provimux-test-session"); state == "bound" {
		t.Fatal("restored although the read never returned")
	}
	provimuxAffinityBeforeRead.Store(nil)
	close(release)
	after := pmxWrite(s) // waits for the late read to end
	if _, state := pmxBound(s, "provimux-test-session"); state == "bound" {
		t.Fatal("the read that returned after the deadline was used")
	}
	if !after.InEffect || after.LastError != "" || after.RestoredAtStart != nil || !strings.Contains(after.StartNote, "did not answer") {
		t.Fatalf("after the late read: %+v", after)
	}
	s.provimuxAffinityStop()
}

func init() {
	// The other tests are not about the start's deadline: a slow machine must not turn a start into "did not
	// answer".
	provimuxAffinityStartDeadline = time.Minute
}
