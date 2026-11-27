package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
)

// Provimux: finding 54. Nothing of a request body is reachable once its model is read: the model is a string of its
// own, in every form the reader reads (at 494df49e3 a form's model was a part of the string the whole form was read
// into: four seven-byte models kept about 32 MiB alive). Four 8 MiB bodies of each form are sent and their copies let
// go; holding only the four models must not hold the bodies.
func TestProvimuxSentKeepsNothingOfTheBody(t *testing.T) {
	provimuxSentOn(t, true)
	const size, bodies = 8 << 20, 4
	send := func(media string) string {
		body := bytes.Repeat([]byte("a"), size)
		switch media {
		case "application/json":
			copy(body, `{"model":"fixture","padding":"`)
			copy(body[size-2:], `"}`)
		default:
			copy(body, "model=fixture&padding=")
		}
		ctx := WithUpstreamAttemptTracker(WithProvimuxStatementAsked(context.Background()))
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.x.ai/v1/responses", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", media)
		resp, err := httptransport.Decorate(ctx, &http.Client{Transport: finalRetainedSink{}}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		sent, ok := ProvimuxSent(ctx, "xai")
		if !ok || sent.Model != "fixture" {
			t.Fatalf("fixture: %s: model %q, named %v", media, sent.Model, ok)
		}
		return sent.Model
	}
	live := func() int64 {
		runtime.GC()
		runtime.GC()
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		return int64(stats.HeapAlloc)
	}
	for _, media := range []string{"application/json", "application/x-www-form-urlencoded"} {
		t.Run(strings.Split(media, "/")[1], func(t *testing.T) {
			start := live()
			var models []string
			for i := 0; i < bodies; i++ {
				models = append(models, send(media))
			}
			held := live() - start
			runtime.KeepAlive(models)
			t.Logf("%s: holding %d models of %d bytes from %d bodies of %d MiB: %d bytes more on the heap", media, len(models), len(models[0]), bodies, size>>20, held)
			// The bodies together are 32 MiB; one would be 8 MiB.
			if held >= size {
				t.Errorf("the models keep the bodies alive: %d bytes", held)
			}
		})
	}
}

// Finding 55: a body is whole when the transport has read it to its end, or every byte it declares (Go's transport
// sends no more than the declared length, and fails a request whose body holds more). The read that meets the end can
// come after the answer is back; the reader does not wait for it. Fewer bytes than declared, or more, are not whole.
func TestProvimuxSentDeclaredLengthIsWhole(t *testing.T) {
	const body = `{"model":"provimux-test-a"}`
	for _, tc := range []struct {
		name     string
		declared int64
		read     int
		whole    bool
	}{
		{"every declared byte, before the read that meets the end", int64(len(body)), len(body), true},
		{"one byte short of the declared length", int64(len(body)), len(body) - 1, false},
		{"no declared length, before the read that meets the end", -1, len(body), false},
		{"more bytes than declared", int64(len(body) - 2), len(body), false},
		// The length rule of part (6): with a declared length, exactly that many bytes; without one, the end.
		{"fewer bytes than declared, read to the end", int64(len(body) + 3), -1, false},
		{"exactly the declared bytes, read to the end", int64(len(body)), -1, true},
		{"no declared length, read to the end", -1, -1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", io.NopCloser(strings.NewReader(body)))
			if err != nil {
				t.Fatal(err)
			}
			req.ContentLength = tc.declared
			sent, capture := provimuxCaptureBody(req)
			if tc.read < 0 { // to the end, as a transport that reads until EOF
				if _, err = io.ReadAll(sent.Body); err != nil {
					t.Fatal(err)
				}
			} else if _, err = io.ReadFull(sent.Body, make([]byte, tc.read)); err != nil {
				t.Fatal(err)
			}
			got, ok := capture.bytes()
			if ok != tc.whole || (ok && string(got) != body) {
				t.Errorf("whole %v (%q), want %v", ok, got, tc.whole)
			}
		})
	}
}
