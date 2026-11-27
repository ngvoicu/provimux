package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
)

// provimuxConsuming is a transport that reads a request's body to its end, as Go's does, and answers 200.
type provimuxConsuming struct{}

func (provimuxConsuming) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
	}
	return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}, Request: r}, nil
}

// What observing a request costs (the lead's amendment of 2026-11-27 (2), part 3): with models.answered-by off; on,
// for a call whose caller did not ask for the statement (nothing is copied); on and asked (the body is copied while the
// transport reads it, the model read, the copy let go). The body is prepared before the timing.
func BenchmarkProvimuxSentObserve(b *testing.B) {
	// Bodies of exactly 1 KiB, 8 MiB and 64 MiB (the largest the reader reads), and one byte more than that.
	for _, size := range []int{1 << 10, 8 << 20, 64 << 20, 64<<20 + 1} {
		prefix := []byte(`{"model":"provimux-test","input":"`)
		body := append(append([]byte(nil), prefix...), bytes.Repeat([]byte("g"), size-len(prefix)-2)...)
		body = append(body, `"}`...)
		for _, mode := range []string{"off", "on-not-asked", "on-asked"} {
			b.Run(fmt.Sprintf("%d/%s", size, mode), func(b *testing.B) {
				was := logging.SetProvimuxAnsweredBy(mode != "off")
				defer logging.SetProvimuxAnsweredBy(was)
				base := context.Background()
				if mode == "on-asked" {
					base = WithProvimuxStatementAsked(base)
				}
				client := &http.Client{Transport: provimuxConsuming{}}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					ctx := WithUpstreamAttemptTracker(base)
					req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.x.ai/v1/responses", bytes.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					resp, err := httptransport.Decorate(ctx, client).Do(req)
					if err == nil {
						_ = resp.Body.Close()
					}
					ProvimuxSent(ctx, "xai")
				}
			})
		}
	}
}
