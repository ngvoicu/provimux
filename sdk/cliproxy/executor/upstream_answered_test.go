package executor

import (
	"context"
	"fmt"
	"testing"
)

// An attempt's response is its credential's when the attempt reached its upstream and its bytes did not come out of
// plugin code. The two marks over their four states; a nil context; a context without a tracker.
// (DESIGN.md 12.4, test 5.)
func TestUpstreamAnswered(t *testing.T) {
	for state := 0; state < 4; state++ {
		attempted, pluginBytes := state&1 != 0, state&2 != 0
		t.Run(fmt.Sprintf("attempted=%t pluginBytes=%t", attempted, pluginBytes), func(t *testing.T) {
			ctx := WithUpstreamAttemptTracker(context.Background())
			if pluginBytes {
				MarkPluginBytes(ctx)
			}
			if attempted {
				MarkUpstreamAttempt(ctx)
			}
			want := attempted && !pluginBytes
			if got := UpstreamAnswered(ctx); got != want {
				t.Fatalf("UpstreamAnswered = %t, want %t", got, want)
			}
			if got := UpstreamAttempted(ctx); got != attempted {
				t.Fatalf("UpstreamAttempted = %t, want %t: MarkPluginBytes must not change it", got, attempted)
			}
		})
	}
	t.Run("nil context", func(t *testing.T) {
		MarkPluginBytes(nil)       //nolint:staticcheck // nil-safe
		if UpstreamAnswered(nil) { //nolint:staticcheck // nil-safe
			t.Fatal("answered with a nil context")
		}
	})
	t.Run("no tracker", func(t *testing.T) {
		ctx := context.Background()
		MarkUpstreamAttempt(ctx)
		MarkPluginBytes(ctx)
		if UpstreamAnswered(ctx) {
			t.Fatal("answered without a tracker")
		}
	})
	t.Run("each attempt its own tracker", func(t *testing.T) {
		first := WithUpstreamAttemptTracker(context.Background())
		MarkUpstreamAttempt(first)
		MarkPluginBytes(first)
		second := WithUpstreamAttemptTracker(first)
		MarkUpstreamAttempt(second)
		if UpstreamAnswered(first) || !UpstreamAnswered(second) {
			t.Fatalf("first %t, second %t: want the marked attempt unnamed and the next one named", UpstreamAnswered(first), UpstreamAnswered(second))
		}
	})
}
