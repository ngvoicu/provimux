package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"runtime"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
)

// provimuxConsuming is a transport that reads a request's body to its end, as Go's does, and answers 200.
type provimuxConsuming struct{}

func (provimuxConsuming) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
	}
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: http.Header{}, Request: r}, nil
}

// What the entry costs per request with models.answered-by on (the default): once a 32 MiB request's response headers
// are back, the attempt holds its model and nothing of its body. The body the caller built stays alive through the
// test; the heap may not hold a second copy of it beside that.
func TestProvimuxSentHoldsNoCopyOfTheBody(t *testing.T) {
	provimuxSentOn(t, true)
	const size = 32 << 20
	prefix := []byte(`{"model":"provimux-test","input":"`)
	body := append(append(append([]byte(nil), prefix...), bytes.Repeat([]byte("g"), size-len(prefix)-2)...), `"}`...)
	live := func() uint64 {
		runtime.GC()
		runtime.GC()
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		return stats.HeapAlloc
	}
	ctx := WithUpstreamAttemptTracker(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.x.ai/v1/responses", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	before := live()
	resp, err := httptransport.Decorate(ctx, &http.Client{Transport: provimuxConsuming{}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	after := live()
	sent, ok := ProvimuxSent(ctx, "xai")
	if !ok || sent.Model != "provimux-test" {
		t.Fatalf("fixture: %+v %v", sent, ok)
	}
	if after > before && after-before > size/8 {
		t.Fatalf("the attempt holds %d bytes more once the headers are back, for a body of %d", after-before, size)
	}
	runtime.KeepAlive(body)
	runtime.KeepAlive(ctx)
}
