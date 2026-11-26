package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	"google.golang.org/protobuf/encoding/protowire"
)

// Provimux: what an attempt sent (answered-by.md 4.2 and 4.3, step 2). The reader of a model, per wire form, and the
// rule that turns an attempt's observations into what was sent or into nothing. Every id, host and key is invented.

func provimuxSentOn(t *testing.T, on bool) {
	t.Helper()
	was := logging.SetProvimuxAnsweredBy(on)
	t.Cleanup(func() { logging.SetProvimuxAnsweredBy(was) })
}

// standIn is a transport that answers every request with status and records the bytes it received.
type standIn struct {
	status   int
	received [][]byte
	closed   atomic.Int32
}

func (s *standIn) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	s.received = append(s.received, body)
	return &http.Response{StatusCode: s.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), Request: req}, nil
}

func (s *standIn) CloseIdleConnections() { s.closed.Add(1) }

// send sends req with a client built as the executors' helpers build one: Decorate after the transport is chosen.
func send(t *testing.T, ctx context.Context, transport http.RoundTripper, req *http.Request) {
	t.Helper()
	client := httptransport.Decorate(ctx, &http.Client{Transport: transport})
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func jsonRequest(t *testing.T, url, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

func devinChat(t *testing.T, fields func(b []byte) []byte) *http.Request {
	t.Helper()
	message := fields(nil)
	frame := append([]byte{0, 0, 0, 0, 0}, message...)
	frame[1], frame[2], frame[3], frame[4] = byte(len(message)>>24), byte(len(message)>>16), byte(len(message)>>8), byte(len(message))
	req, err := http.NewRequest(http.MethodPost, "https://server.codeium.com"+provimuxDevinChatMethod, bytes.NewReader(frame))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/connect+proto")
	return req
}

func TestProvimuxSentModelPerWireForm(t *testing.T) {
	zstdBody := func(body string) []byte {
		encoder, _ := zstd.NewWriter(nil)
		defer encoder.Close()
		return encoder.EncodeAll([]byte(body), nil)
	}
	gzipBody := func(body string) []byte {
		var buffer bytes.Buffer
		writer := gzip.NewWriter(&buffer)
		_, _ = writer.Write([]byte(body))
		_ = writer.Close()
		return buffer.Bytes()
	}
	multipartBody := func() (*bytes.Buffer, string) {
		var buffer bytes.Buffer
		writer := multipart.NewWriter(&buffer)
		_ = writer.WriteField("model", "provimux-test-image")
		_ = writer.Close()
		return &buffer, writer.FormDataContentType()
	}
	for _, tc := range []struct {
		name   string
		req    func(t *testing.T) *http.Request
		model  string
		unread bool
	}{
		{"JSON body", func(t *testing.T) *http.Request {
			return jsonRequest(t, "https://api.anthropic.com/v1/messages", `{"model":"provimux-test-a","messages":[{"model":"inner"}]}`)
		}, "provimux-test-a", false},
		{"JSON body without a content type", func(t *testing.T) *http.Request {
			req := jsonRequest(t, "https://api.x.ai/v1/responses", ` {"model":"provimux-test-a"}`)
			req.Header.Del("Content-Type")
			return req
		}, "provimux-test-a", false},
		{"JSON body, an escaped key", func(t *testing.T) *http.Request {
			return jsonRequest(t, "https://api.anthropic.com/v1/messages", `{"mod\u0065l":"provimux-test-a"}`)
		}, "provimux-test-a", false},
		{"JSON body with no model (a discovery)", func(t *testing.T) *http.Request {
			return jsonRequest(t, "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist", `{"metadata":{}}`)
		}, "", false},
		{"no body (a token exchange)", func(t *testing.T) *http.Request {
			req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/copilot_internal/v2/token", nil)
			return req
		}, "", false},
		{"two model keys", func(t *testing.T) *http.Request {
			return jsonRequest(t, "https://api.anthropic.com/v1/messages", `{"model":"provimux-test-a","model":"provimux-test-b"}`)
		}, "", true},
		{"two model keys, one escaped", func(t *testing.T) *http.Request {
			return jsonRequest(t, "https://api.anthropic.com/v1/messages", `{"model":"provimux-test-a","mod\u0065l":"provimux-test-a"}`)
		}, "", true},
		{"a model that is not a string", func(t *testing.T) *http.Request {
			return jsonRequest(t, "https://api.anthropic.com/v1/messages", `{"model":{"id":"provimux-test-a"}}`)
		}, "", true},
		{"an empty model", func(t *testing.T) *http.Request {
			return jsonRequest(t, "https://api.anthropic.com/v1/messages", `{"model":""}`)
		}, "", true},
		{"a JSON array", func(t *testing.T) *http.Request {
			return jsonRequest(t, "https://api.anthropic.com/v1/messages", `[{"model":"provimux-test-a"}]`)
		}, "", true},
		{"not JSON though it says so", func(t *testing.T) *http.Request {
			return jsonRequest(t, "https://api.anthropic.com/v1/messages", `{"model":"provimux-test-a"`)
		}, "", true},
		{"zstd body", func(t *testing.T) *http.Request {
			req, _ := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(zstdBody(`{"model":"provimux-test-z"}`)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Encoding", "zstd")
			return req
		}, "provimux-test-z", false},
		{"gzip body: an encoding the reader does not decode", func(t *testing.T) *http.Request {
			req, _ := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(gzipBody(`{"model":"provimux-test-z"}`)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Encoding", "gzip")
			return req
		}, "", true},
		{"a body without GetBody", func(t *testing.T) *http.Request {
			req := jsonRequest(t, "https://api.anthropic.com/v1/messages", `{"model":"provimux-test-a"}`)
			req.Body = io.NopCloser(strings.NewReader(`{"model":"provimux-test-a"}`))
			req.GetBody = nil
			return req
		}, "", true},
		{"a multipart upload", func(t *testing.T) *http.Request {
			buffer, contentType := multipartBody()
			req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/images/edits", buffer)
			req.Header.Set("Content-Type", contentType)
			return req
		}, "", true},
		{"a form with a model", func(t *testing.T) *http.Request {
			req, _ := http.NewRequest(http.MethodPost, "https://provimux-test.example/v1/x", strings.NewReader("model=provimux-test-f&x=1"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return req
		}, "provimux-test-f", false},
		{"a token form", func(t *testing.T) *http.Request {
			req, _ := http.NewRequest(http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader("grant_type=provimux-test&assertion=x"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return req
		}, "", false},
		{"Gemini: the id in the path", func(t *testing.T) *http.Request {
			return jsonRequest(t, "https://generativelanguage.googleapis.com/v1beta/models/provimux-test-g:streamGenerateContent?alt=sse", `{"contents":[]}`)
		}, "provimux-test-g", false},
		{"Vertex native: Google's publisher", func(t *testing.T) *http.Request {
			return jsonRequest(t, "https://aiplatform.googleapis.com/v1/projects/p/locations/us/publishers/google/models/provimux-test-v:generateContent", `{"contents":[]}`)
		}, "provimux-test-v", false},
		{"Vertex native: another publisher", func(t *testing.T) *http.Request {
			return jsonRequest(t, "https://aiplatform.googleapis.com/v1/projects/p/locations/us/publishers/acme/models/provimux-test-v:rawPredict", `{}`)
		}, "", true},
		{"the path and the body agree", func(t *testing.T) *http.Request {
			return jsonRequest(t, "https://generativelanguage.googleapis.com/v1beta/models/provimux-test-g:generateContent", `{"model":"provimux-test-g"}`)
		}, "provimux-test-g", false},
		{"the path and the body differ", func(t *testing.T) *http.Request {
			return jsonRequest(t, "https://generativelanguage.googleapis.com/v1beta/models/provimux-test-g:generateContent", `{"model":"provimux-test-h"}`)
		}, "", true},
		{"Devin: field 21 of the chat message", func(t *testing.T) *http.Request {
			return devinChat(t, func(b []byte) []byte {
				b = protowire.AppendTag(b, 16, protowire.BytesType)
				b = protowire.AppendString(b, "provimux-test-cascade")
				b = protowire.AppendTag(b, 20, protowire.VarintType)
				b = protowire.AppendVarint(b, 1)
				b = protowire.AppendTag(b, 21, protowire.BytesType)
				return protowire.AppendString(b, "provimux-test-d_HIGH")
			})
		}, "provimux-test-d_HIGH", false},
		{"Devin: a chat message without field 21", func(t *testing.T) *http.Request {
			return devinChat(t, func(b []byte) []byte {
				b = protowire.AppendTag(b, 16, protowire.BytesType)
				return protowire.AppendString(b, "provimux-test-cascade")
			})
		}, "", true},
		{"Devin: field 21 twice, differing", func(t *testing.T) *http.Request {
			return devinChat(t, func(b []byte) []byte {
				b = protowire.AppendTag(b, 21, protowire.BytesType)
				b = protowire.AppendString(b, "provimux-test-d")
				b = protowire.AppendTag(b, 21, protowire.BytesType)
				return protowire.AppendString(b, "provimux-test-e")
			})
		}, "", true},
		{"Devin: field 21 not a string", func(t *testing.T) *http.Request {
			return devinChat(t, func(b []byte) []byte {
				b = protowire.AppendTag(b, 21, protowire.VarintType)
				return protowire.AppendVarint(b, 7)
			})
		}, "", true},
		{"Devin: a frame that is not the whole body", func(t *testing.T) *http.Request {
			req := devinChat(t, func(b []byte) []byte {
				b = protowire.AppendTag(b, 21, protowire.BytesType)
				return protowire.AppendString(b, "provimux-test-d")
			})
			body, _ := req.GetBody()
			raw, _ := io.ReadAll(body)
			raw = append(raw, 0, 0, 0, 0, 0)
			again, _ := http.NewRequest(http.MethodPost, req.URL.String(), bytes.NewReader(raw))
			again.Header = req.Header
			return again
		}, "", true},
		{"Devin: another method carries no model", func(t *testing.T) *http.Request {
			req, _ := http.NewRequest(http.MethodPost, "https://server.codeium.com/exa.api_server_pb.ApiServerService/GetUserStatus", bytes.NewReader([]byte{0, 0, 0, 0, 0}))
			req.Header.Set("Content-Type", "application/connect+proto")
			return req
		}, "", false},
		{"a body in a form the reader does not know", func(t *testing.T) *http.Request {
			req, _ := http.NewRequest(http.MethodPost, "https://provimux-test.example/v1/x", strings.NewReader("model: provimux-test-a"))
			req.Header.Set("Content-Type", "text/plain")
			return req
		}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req(t)
			model, unread := provimuxReadModel(req)
			if model != tc.model || unread != tc.unread {
				t.Fatalf("read %q unread=%v, want %q unread=%v", model, unread, tc.model, tc.unread)
			}
			if got := provimuxSentModel(req); got != tc.model {
				t.Fatalf("provimuxSentModel %q, want %q", got, tc.model)
			}
		})
	}
}

// The decorator reads and never consumes: the stand-in receives every byte, and the request's body is the one the
// client handed over.
func TestProvimuxSentDecoratorChangesNothing(t *testing.T) {
	provimuxSentOn(t, true)
	const body = `{"model":"provimux-test-a","messages":[]}`
	ctx := WithUpstreamAttemptTracker(context.Background())
	stand := &standIn{status: http.StatusOK}
	send(t, ctx, stand, jsonRequest(t, "https://api.anthropic.com/v1/messages", body))
	if len(stand.received) != 1 || string(stand.received[0]) != body {
		t.Fatalf("the stand-in received %q, want %q", stand.received, body)
	}
	sent, ok := ProvimuxSent(ctx)
	want := ProvimuxSentRequest{Scheme: "https", Host: "api.anthropic.com", Model: "provimux-test-a", Status: http.StatusOK}
	if !ok || sent != want {
		t.Fatalf("sent %+v %v, want %+v", sent, ok, want)
	}
	// The client's call to close idle connections reaches the transport below.
	httptransport.Decorate(ctx, &http.Client{Transport: stand}).CloseIdleConnections()
	if stand.closed.Load() != 1 {
		t.Fatal("CloseIdleConnections did not reach the transport")
	}
}

// With models.answered-by off the decorator is not attached: the client is the one the helper built.
func TestProvimuxSentOffAttachesNothing(t *testing.T) {
	provimuxSentOn(t, false)
	ctx := WithUpstreamAttemptTracker(context.Background())
	client := &http.Client{Transport: &standIn{status: http.StatusOK}}
	if httptransport.Decorate(ctx, client) != client {
		t.Fatal("a decorator is attached with the switch off")
	}
	send(t, ctx, client.Transport, jsonRequest(t, "https://api.anthropic.com/v1/messages", `{"model":"provimux-test-a"}`))
	if _, ok := ProvimuxSent(ctx); ok {
		t.Fatal("something was observed with the switch off")
	}
}

// recordingDecorator is an embedder's decorator: it counts the requests it sees.
type recordingDecorator struct {
	next http.RoundTripper
	seen *atomic.Int32
}

func (d recordingDecorator) RoundTrip(req *http.Request) (*http.Response, error) {
	d.seen.Add(1)
	return d.next.RoundTrip(req)
}

// A decorator an embedder put on the context stays, inside ours; a tracker attached again (a refresh, a retry's new
// attempt) does not observe a request twice, and each request lands on the tracker of its own context.
func TestProvimuxSentKeepsAnEmbeddersDecoratorAndNests(t *testing.T) {
	provimuxSentOn(t, true)
	var seen atomic.Int32
	base := httptransport.WithDecorator(context.Background(), func(next http.RoundTripper) http.RoundTripper {
		return recordingDecorator{next: next, seen: &seen}
	})
	first := WithUpstreamAttemptTracker(base)
	second := WithUpstreamAttemptTracker(first)
	stand := &standIn{status: http.StatusOK}
	send(t, second, stand, jsonRequest(t, "https://api.anthropic.com/v1/messages", `{"model":"provimux-test-a"}`))
	if seen.Load() != 1 {
		t.Fatalf("the embedder's decorator saw %d requests, want 1", seen.Load())
	}
	if _, ok := ProvimuxSent(first); ok {
		t.Fatal("a request of the second attempt landed on the first")
	}
	log := provimuxSentLogOf(second)
	if sent, ok := ProvimuxSent(second); !ok || sent.Model != "provimux-test-a" || len(log.requests) != 1 {
		t.Fatalf("the second attempt: %+v %v, %d observations", sent, ok, len(log.requests))
	}
}

// Step 2 of the rule, over the observations an attempt can hold.
func TestProvimuxSentTheRule(t *testing.T) {
	answered := ProvimuxSentRequest{Scheme: "https", Host: "api.x.ai", Model: "provimux-test-a", Signer: "authorization", Status: 200}
	with := func(change func(*ProvimuxSentRequest)) ProvimuxSentRequest {
		r := answered
		change(&r)
		return r
	}
	for _, tc := range []struct {
		name     string
		requests []ProvimuxSentRequest
		overflow bool
		want     bool
	}{
		{"none", nil, false, false},
		{"one answered model request", []ProvimuxSentRequest{answered}, false, true},
		{"a refusal, then the same again answered", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.Status = 400 }), answered}, false, true},
		{"a redirect, then the host that answered", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.Status = 307; r.Host = "provimux-test.example" }), answered}, false, true},
		{"a transport error, then the same answered", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.Status = 0 }), answered}, false, true},
		{"a token exchange without a model beside it", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.Model = ""; r.Host = "auth.example.test" }), answered}, false, true},
		{"the same answered twice", []ProvimuxSentRequest{answered, answered}, false, true},
		{"two answered requests, two models", []ProvimuxSentRequest{answered, with(func(r *ProvimuxSentRequest) { r.Model = "provimux-test-b" })}, false, false},
		{"two answered requests, two hosts", []ProvimuxSentRequest{answered, with(func(r *ProvimuxSentRequest) { r.Host = "cli-chat-proxy.grok.com" })}, false, false},
		{"two answered requests, two ports", []ProvimuxSentRequest{answered, with(func(r *ProvimuxSentRequest) { r.Port = "8443" })}, false, false},
		{"two answered requests, two signers", []ProvimuxSentRequest{answered, with(func(r *ProvimuxSentRequest) { r.Signer = "x-api-key" })}, false, false},
		{"an answered request it could not read", []ProvimuxSentRequest{answered, with(func(r *ProvimuxSentRequest) { r.Model = ""; r.unread = true })}, false, false},
		{"a refused request it could not read", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.Model = ""; r.unread = true; r.Status = 400 }), answered}, false, true},
		{"only refusals", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.Status = 500 })}, false, false},
		{"more than the tracker keeps", []ProvimuxSentRequest{answered}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), upstreamAttemptTrackerContextKey{}, &upstreamAttemptTracker{})
			log := provimuxSentLogOf(ctx)
			for _, request := range tc.requests {
				log.add(request)
			}
			log.overflow = log.overflow || tc.overflow
			sent, ok := ProvimuxSent(ctx)
			if ok != tc.want || (ok && sent != answered) {
				t.Fatalf("ProvimuxSent = %+v %v, want %v", sent, ok, tc.want)
			}
		})
	}
	// The tracker keeps provimuxSentKept requests; one more and nothing is known.
	ctx := context.WithValue(context.Background(), upstreamAttemptTrackerContextKey{}, &upstreamAttemptTracker{})
	for i := 0; i <= provimuxSentKept; i++ {
		provimuxSentLogOf(ctx).add(answered)
	}
	if _, ok := ProvimuxSent(ctx); ok {
		t.Fatal("an attempt with more requests than the tracker keeps names a model")
	}
	if _, ok := ProvimuxSent(context.Background()); ok {
		t.Fatal("a context without a tracker names a model")
	}
}

// The observation's URL and signer: lower case, the default port as "", the credential headers by name in any case.
func TestProvimuxSentObservation(t *testing.T) {
	for _, tc := range []struct {
		url    string
		header http.Header
		want   ProvimuxSentRequest
	}{
		{"https://API.Anthropic.com:443/v1/messages", http.Header{"x-api-key": {"k"}}, ProvimuxSentRequest{Scheme: "https", Host: "api.anthropic.com", Signer: "x-api-key"}},
		{"https://api.anthropic.com/v1/messages", http.Header{"Authorization": {"Bearer t"}}, ProvimuxSentRequest{Scheme: "https", Host: "api.anthropic.com", Signer: "authorization"}},
		{"https://api.anthropic.com/v1/messages", http.Header{"X-Api-Key": {"k"}, "authorization": {"Bearer t"}}, ProvimuxSentRequest{Scheme: "https", Host: "api.anthropic.com", Signer: "both"}},
		{"http://api.anthropic.com:80/v1/messages", http.Header{}, ProvimuxSentRequest{Scheme: "http", Host: "api.anthropic.com"}},
		{"https://api.anthropic.com:8443/v1/messages", http.Header{}, ProvimuxSentRequest{Scheme: "https", Host: "api.anthropic.com", Port: "8443"}},
	} {
		req, _ := http.NewRequest(http.MethodGet, tc.url, nil)
		req.Header = tc.header
		if got := provimuxObserveRequest(req); got != tc.want {
			t.Errorf("%s %v: %+v, want %+v", tc.url, tc.header, got, tc.want)
		}
	}
}
