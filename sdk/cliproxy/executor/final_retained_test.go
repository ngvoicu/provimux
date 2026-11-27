package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
)

type finalRetainedSink struct{ replay bool }

func (s finalRetainedSink) RoundTrip(r *http.Request) (*http.Response, error) {
	drain := func(body io.ReadCloser) error {
		_, err := io.Copy(io.Discard, body)
		body.Close()
		return err
	}
	if err := drain(r.Body); err != nil {
		return nil, err
	}
	if s.replay {
		copy, err := r.GetBody()
		if err != nil {
			return nil, err
		}
		if err = drain(copy); err != nil {
			return nil, err
		}
	}
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
}

// The copy of a request body (the lead's amendment of 2026-11-27 (2), part 3). Ported: the reviewer measured what the
// attempt kept after the model was read (all of it, and twice for a replay); the decided behaviour is that nothing is
// copied for a call whose caller did not ask for the statement, and that the copy goes once the model is read.
func TestFinalRetainedWireBytes(t *testing.T) {
	provimuxSentOn(t, true)
	for _, tc := range []struct {
		size   int
		replay bool
	}{{8 << 20, false}, {64 << 20, false}, {8 << 20, true}} {
		t.Run(fmt.Sprintf("%d-replay-%v", tc.size, tc.replay), func(t *testing.T) {
			data := bytes.Repeat([]byte(" "), tc.size)
			copy(data, []byte("{\"model\":\"fixture\"}"))
			unasked := WithUpstreamAttemptTracker(context.Background())
			plain, _ := http.NewRequestWithContext(unasked, "POST", "https://api.x.ai/v1/responses", bytes.NewReader(data))
			plain.Header.Set("Content-Type", "application/json")
			if out, err := httptransport.Decorate(unasked, &http.Client{Transport: finalRetainedSink{tc.replay}}).Do(plain); err == nil {
				out.Body.Close()
			}
			if log := provimuxSentLogOf(unasked); len(log.requests) != 1 || log.requests[0].body != nil {
				t.Fatal("a body was copied for a call that did not ask for the statement")
			}
			ctx := WithUpstreamAttemptTracker(WithProvimuxStatementAsked(context.Background()))
			req, err := http.NewRequestWithContext(ctx, "POST", "https://api.x.ai/v1/responses", bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			out, err := httptransport.Decorate(ctx, &http.Client{Transport: finalRetainedSink{tc.replay}}).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			out.Body.Close()
			model, ok := ProvimuxSent(ctx, "xai")
			if !ok || model.Model != "fixture" {
				t.Fatal("observation fixture")
			}
			capture := provimuxSentLogOf(ctx).requests[0].body
			var retained, capacity int
			for _, stream := range capture.streams {
				retained += stream.data.Len()
				capacity += stream.data.Cap()
			}
			factor := 1
			if tc.replay {
				factor++
			}
			t.Logf("payload=%d reads=%d retained after model extraction=%d bytes of data in %d bytes of buffer capacity", tc.size, factor, retained, capacity)
			if retained != 0 || capacity != 0 || !capture.released {
				t.Errorf("the copy was kept after the model was read")
			}
		})
	}
}
