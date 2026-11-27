package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// Provimux: a try that a later try in the same attempt replaced (ProvimuxSupersedeSent). A caller that sends the
// attempt's request again on the same account, after a first try whose response began with a 200 and failed inside
// its body, marks what the attempt logged so far; the second try alone is then the candidate. Every id and host is
// invented.

// scriptedStandIn answers the requests it receives with the given statuses, in order, reading each body whole.
type scriptedStandIn struct {
	mu       sync.Mutex
	statuses []int
}

func (s *scriptedStandIn) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	s.mu.Lock()
	status := s.statuses[0]
	if len(s.statuses) > 1 {
		s.statuses = s.statuses[1:]
	}
	s.mu.Unlock()
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), Request: req}, nil
}

func TestProvimuxSupersedeSent(t *testing.T) {
	provimuxSentOn(t, true)
	const url = "https://api.x.ai/v1/responses"
	type step struct {
		model     string // a request with this model; "" = the call ProvimuxSupersedeSent
		supersede bool
	}
	try := func(model string) step { return step{model: model} }
	supersede := step{supersede: true}
	for _, tc := range []struct {
		name     string
		statuses []int
		steps    []step
		want     string // the model named; "" = none
	}{
		{"control: one answered try", []int{200}, []step{try("provimux-test-a")}, "provimux-test-a"},
		{"control: a first try refused with an error status, then the same again", []int{529, 200}, []step{try("provimux-test-a"), try("provimux-test-a")}, "provimux-test-a"},
		{"control: a first try answered 200, then the same again, not marked", []int{200, 200}, []step{try("provimux-test-a"), try("provimux-test-a")}, ""},
		{"a first try answered 200, marked, then the same again", []int{200, 200}, []step{try("provimux-test-a"), supersede, try("provimux-test-a")}, "provimux-test-a"},
		{"a first try marked, then a second with another model", []int{200, 200}, []step{try("provimux-test-a"), supersede, try("provimux-test-b")}, "provimux-test-b"},
		{"marked twice, then a third try", []int{200, 200, 200}, []step{try("provimux-test-a"), supersede, try("provimux-test-a"), supersede, try("provimux-test-a")}, "provimux-test-a"},
		{"marked, and no second try", []int{200}, []step{try("provimux-test-a"), supersede}, ""},
		{"marked after the second try: both are left out", []int{200, 200}, []step{try("provimux-test-a"), try("provimux-test-a"), supersede}, ""},
		{"marked, then two tries answered", []int{200, 200, 200}, []step{try("provimux-test-a"), supersede, try("provimux-test-a"), try("provimux-test-a")}, ""},
		{"marked, then a second try refused", []int{200, 503}, []step{try("provimux-test-a"), supersede, try("provimux-test-a")}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithUpstreamAttemptTracker(WithProvimuxStatementAsked(context.Background()))
			stand := &scriptedStandIn{statuses: tc.statuses}
			for _, s := range tc.steps {
				if s.supersede {
					ProvimuxSupersedeSent(ctx)
					continue
				}
				send(t, ctx, stand, jsonRequest(t, url, `{"model":"`+s.model+`"}`))
			}
			sent, ok := ProvimuxSent(ctx, "xai")
			if got := map[bool]string{true: sent.Model}[ok]; got != tc.want || ok != (tc.want != "") {
				t.Fatalf("ProvimuxSent = %q %v, want %q", sent.Model, ok, tc.want)
			}
			if ok && (sent.Host != "api.x.ai" || sent.Status != 200) {
				t.Fatalf("ProvimuxSent = %+v", public(sent))
			}
		})
	}
}

// What is marked lets its copy of the body go at once, and only the attempt of ctx is marked: another attempt's log,
// a context without a log and a nil context are left as they are.
func TestProvimuxSupersedeSentScope(t *testing.T) {
	provimuxSentOn(t, true)
	const url = "https://api.x.ai/v1/responses"
	first := WithUpstreamAttemptTracker(WithProvimuxStatementAsked(context.Background()))
	other := WithUpstreamAttemptTracker(WithProvimuxStatementAsked(context.Background()))
	send(t, first, &scriptedStandIn{statuses: []int{200}}, jsonRequest(t, url, `{"model":"provimux-test-a"}`))
	send(t, other, &scriptedStandIn{statuses: []int{200}}, jsonRequest(t, url, `{"model":"provimux-test-b"}`))

	ProvimuxSupersedeSent(first)
	ProvimuxSupersedeSent(context.Background())
	ProvimuxSupersedeSent(nil) //nolint:staticcheck // a nil context does nothing

	log := provimuxSentLogOf(first)
	capture := log.requests[0].body
	capture.mu.Lock()
	kept := 0
	for _, stream := range capture.streams {
		kept += stream.data.Cap()
	}
	released := capture.released
	capture.mu.Unlock()
	if !log.requests[0].superseded || !released || kept != 0 {
		t.Fatalf("marked %v, copy let go %v, %d bytes kept", log.requests[0].superseded, released, kept)
	}
	if _, ok := ProvimuxSent(first, "xai"); ok {
		t.Fatal("the marked attempt names a model without a later try")
	}
	if sent, ok := ProvimuxSent(other, "xai"); !ok || sent.Model != "provimux-test-b" {
		t.Fatalf("another attempt: %q %v, want its own model", sent.Model, ok)
	}

	// A body asked for again after its copy was let go is not copied.
	capture.tee(io.NopCloser(strings.NewReader(`{"model":"provimux-test-a"}`)), 27)
	capture.mu.Lock()
	last := capture.streams[len(capture.streams)-1]
	failed, size := last.failed, last.data.Cap()
	capture.mu.Unlock()
	if !failed || size != 0 {
		t.Fatalf("a body after the release: failed %v, %d bytes", failed, size)
	}
}

// With models.answered-by off nothing is logged, and the call does nothing.
func TestProvimuxSupersedeSentOff(t *testing.T) {
	provimuxSentOn(t, false)
	ctx := WithUpstreamAttemptTracker(WithProvimuxStatementAsked(context.Background()))
	send(t, ctx, &scriptedStandIn{statuses: []int{200}}, jsonRequest(t, "https://api.x.ai/v1/responses", `{"model":"provimux-test-a"}`))
	ProvimuxSupersedeSent(ctx)
	if provimuxSentLogOf(ctx) != nil {
		t.Fatal("a log with the switch off")
	}
}
