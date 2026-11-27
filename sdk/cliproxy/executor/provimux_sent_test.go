package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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

// ProvimuxPassesRequestsUnchanged: the stand-in hands nothing on; it receives what left.
func (s *standIn) ProvimuxPassesRequestsUnchanged() bool { return true }

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

// recordingDecorator is an embedder's decorator: it counts the requests it sees. vouches says whether it declares
// that it hands requests on unchanged.
type recordingDecorator struct {
	next    http.RoundTripper
	seen    *atomic.Int32
	vouches bool
}

func (d recordingDecorator) RoundTrip(req *http.Request) (*http.Response, error) {
	d.seen.Add(1)
	return d.next.RoundTrip(req)
}

type vouchingDecorator struct{ recordingDecorator }

func (vouchingDecorator) ProvimuxPassesRequestsUnchanged() bool { return true }

// A decorator an embedder put on the context stays, inside ours, and sees each request once; it is a transport below
// the reader that is not the gateway's own, so the attempt names nothing unless it vouches that it hands requests on
// unchanged. An attempt inside another (a refresh) notes its requests on its own log, not on the enclosing one's.
func TestProvimuxSentKeepsAnEmbeddersDecoratorAndNests(t *testing.T) {
	provimuxSentOn(t, true)
	for _, vouches := range []bool{false, true} {
		var seen atomic.Int32
		base := httptransport.WithDecorator(context.Background(), func(next http.RoundTripper) http.RoundTripper {
			if vouches {
				return vouchingDecorator{recordingDecorator{next: next, seen: &seen}}
			}
			return recordingDecorator{next: next, seen: &seen}
		})
		first := WithUpstreamAttemptTracker(base)
		second := WithUpstreamAttemptTracker(first)
		stand := &standIn{status: http.StatusOK}
		send(t, second, stand, jsonRequest(t, "https://api.anthropic.com/v1/messages", `{"model":"provimux-test-a"}`))
		if seen.Load() != 1 {
			t.Fatalf("the embedder's decorator saw %d requests, want 1", seen.Load())
		}
		if log := provimuxSentLogOf(first); len(log.requests) != 0 {
			t.Fatalf("a request of the inner attempt landed on the outer: %+v", log.requests)
		}
		log := provimuxSentLogOf(second)
		sent, ok := ProvimuxSent(second)
		if len(log.requests) != 1 || ok != vouches || (ok && sent.Model != "provimux-test-a") {
			t.Fatalf("vouches %v: %+v %v, %d observations", vouches, sent, ok, len(log.requests))
		}
	}
}

// The rule (the lead's of 2026-11-27): exactly one request that could have produced the answer, read whole, nothing
// making what was read differ from what left. Housekeeping and refusals do not count; everything else does.
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
		foreign  bool
		want     bool
	}{
		{"none", nil, false, false, false},
		{"one answered model request", []ProvimuxSentRequest{answered}, false, false, true},
		{"a refusal, then the same again answered", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.Status = 400 }), answered}, false, false, true},
		{"a redirect, then the host that answered", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.Status = 307; r.Host = "provimux-test.example" }), answered}, false, false, true},
		{"a transport error, then the same answered", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.Status = 0 }), answered}, false, false, true},
		{"a housekeeping request beside it", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.Model = ""; r.Host = "oauth2.googleapis.com"; r.housekeeping = true }), answered}, false, false, true},
		{"an answered request without a model, not housekeeping", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.Model = ""; r.Host = "auth.example.test" }), answered}, false, false, false},
		{"the same answered twice", []ProvimuxSentRequest{answered, answered}, false, false, false},
		{"two answered requests, two models", []ProvimuxSentRequest{answered, with(func(r *ProvimuxSentRequest) { r.Model = "provimux-test-b" })}, false, false, false},
		{"an answered request it could not read", []ProvimuxSentRequest{answered, with(func(r *ProvimuxSentRequest) { r.Model = ""; r.unread = true })}, false, false, false},
		{"the one request unread", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.unread = true })}, false, false, false},
		{"the one request untrusted", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.untrusted = true })}, false, false, false},
		{"the one request without a model", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.Model = "" })}, false, false, false},
		{"a refused request it could not read", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.Model = ""; r.unread = true; r.Status = 400 }), answered}, false, false, true},
		{"only refusals", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.Status = 500 })}, false, false, false},
		{"more than the log keeps", []ProvimuxSentRequest{answered}, true, false, false},
		{"a connection the decorator did not see", []ProvimuxSentRequest{answered}, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := &provimuxSentLog{}
			ctx := context.WithValue(context.Background(), provimuxSentLogKey{}, log)
			for _, request := range tc.requests {
				log.add(request)
			}
			log.overflow = log.overflow || tc.overflow
			if tc.foreign {
				log.conns.Add(1)
			}
			sent, ok := ProvimuxSent(ctx)
			if ok != tc.want || (ok && sent != answered) {
				t.Fatalf("ProvimuxSent = %+v %v, want %v", sent, ok, tc.want)
			}
		})
	}
	if _, ok := ProvimuxSent(context.Background()); ok {
		t.Fatal("a context without a log names a model")
	}
}

// A request on another context than the attempt's (detached) is noted on the attempt as untrusted: the attempt
// names nothing.
func TestProvimuxSentDetachedRequestCancels(t *testing.T) {
	provimuxSentOn(t, true)
	ctx := WithUpstreamAttemptTracker(context.Background())
	stand := &standIn{status: http.StatusOK}
	client := httptransport.Decorate(ctx, &http.Client{Transport: stand})
	for _, requestCtx := range []context.Context{ctx, context.Background()} {
		req := jsonRequest(t, "https://api.x.ai/v1/responses", `{"model":"provimux-test-a"}`)
		resp, err := client.Do(req.WithContext(requestCtx))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		_, ok := ProvimuxSent(ctx)
		if want := requestCtx == ctx; ok != want {
			t.Fatalf("after a request on the attempt's context %v: named %v", requestCtx == ctx, ok)
		}
	}
}

// What leaves is what was read: a Host other than the URL's, and a transport below the reader that is not the
// gateway's own (on the client or as the context's per-credential transport), give nothing.
func TestProvimuxSentWhatLeaves(t *testing.T) {
	provimuxSentOn(t, true)
	type plain struct{ http.RoundTripper }
	for _, tc := range []struct {
		name string
		req  func(*http.Request)
		rt   func(stand *standIn) http.RoundTripper
		ctx  func(context.Context, *standIn) context.Context
		want bool
	}{
		{name: "the URL's host", want: true},
		{name: "the URL's host as Host, with the default port", req: func(r *http.Request) { r.Host = "API.x.ai:443" }, want: true},
		{name: "another Host", req: func(r *http.Request) { r.Host = "routing.provimux-test.invalid" }},
		{name: "another port in Host", req: func(r *http.Request) { r.Host = "api.x.ai:8443" }},
		{name: "a transport that does not vouch", rt: func(stand *standIn) http.RoundTripper { return plain{stand} }},
		{name: "Go's transport", rt: func(*standIn) http.RoundTripper { return &http.Transport{} }, want: true},
		{name: "a context transport that does not vouch", ctx: func(c context.Context, stand *standIn) context.Context {
			return context.WithValue(c, "cliproxy.roundtripper", http.RoundTripper(plain{stand}))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithUpstreamAttemptTracker(context.Background())
			stand := &standIn{status: http.StatusOK}
			var rt http.RoundTripper = stand
			if tc.rt != nil {
				rt = tc.rt(stand)
			}
			if tc.ctx != nil {
				ctx = tc.ctx(ctx, stand)
			}
			req := jsonRequest(t, "https://api.x.ai/v1/responses", `{"model":"provimux-test-a"}`)
			if tc.req != nil {
				tc.req(req)
			}
			log := provimuxSentLogOf(ctx)
			observer := &provimuxSentObserver{next: rt, log: log}
			resp, err := observer.RoundTrip(req.WithContext(ctx))
			if err == nil {
				_ = resp.Body.Close()
			}
			if len(log.requests) != 1 {
				t.Fatalf("%d observations", len(log.requests))
			}
			// Go's own transport has nothing to dial here: its error is a refusal, not an answer. Its verdict on the
			// transport is what is asked.
			if tc.name == "Go's transport" {
				if log.requests[0].untrusted {
					t.Fatal("Go's transport is not one of the gateway's own")
				}
				return
			}
			if _, ok := ProvimuxSent(ctx); ok != tc.want {
				t.Fatalf("named %v, want %v (%+v)", ok, tc.want, log.requests)
			}
		})
	}
}

// A connection the attempt's context asks for and no decorated client made (an upstream WebSocket, a client no helper
// built) cancels: here a real loopback request through a client without the decorator, beside one with it.
func TestProvimuxSentUnseenConnectionCancels(t *testing.T) {
	provimuxSentOn(t, true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{}")
	}))
	t.Cleanup(server.Close)
	for _, unseen := range []bool{false, true} {
		ctx := WithUpstreamAttemptTracker(context.Background())
		seen := jsonRequest(t, server.URL+"/v1/responses", `{"model":"provimux-test-a"}`)
		resp, err := httptransport.Decorate(ctx, &http.Client{Transport: &http.Transport{}}).Do(seen.WithContext(ctx))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if unseen {
			other := jsonRequest(t, server.URL+"/v1/side", `{}`)
			resp, err = (&http.Client{Transport: &http.Transport{}}).Do(other.WithContext(ctx))
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
		}
		log := provimuxSentLogOf(ctx)
		if _, ok := ProvimuxSent(ctx); ok == unseen || log.conns.Load() == 0 {
			t.Fatalf("unseen %v: named %v (connections %d, seen %d)", unseen, ok, log.conns.Load(), log.observed.Load())
		}
	}
}

// The closed housekeeping list, by destination and shape: a token form to Google's endpoint, Copilot's token and
// catalog; the same destinations in another shape, or with a model, are candidates.
func TestProvimuxSentHousekeeping(t *testing.T) {
	form := func(method, url, body string) *http.Request {
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return req
	}
	for _, tc := range []struct {
		req  *http.Request
		want bool
	}{
		{form(http.MethodPost, "https://oauth2.googleapis.com/token", "grant_type=provimux-test"), true},
		{form(http.MethodGet, "https://api.github.com/copilot_internal/v2/token", ""), true},
		{form(http.MethodGet, "https://api.individual.githubcopilot.com/models", ""), true},
		{form(http.MethodGet, "https://api.githubcopilot.com/models", ""), true},
		{jsonRequest(t, "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist", `{"metadata":{}}`), true},
		{jsonRequest(t, "https://daily-cloudcode-pa.googleapis.com/v1internal:onboardUser", `{"tierId":"x"}`), true},
		{jsonRequest(t, "https://cloudcode-pa.googleapis.com/v1internal:generateContent", `{"request":{}}`), false},
		{jsonRequest(t, "https://oauth2.googleapis.com/token", `{"grant_type":"x"}`), false},
		{form(http.MethodPost, "https://oauth2.googleapis.com/token", "model=provimux-test-a"), false},
		{form(http.MethodGet, "https://api.githubcopilot.com/models/provimux-test-a", ""), false},
		{form(http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", "a=b"), false},
		{form(http.MethodGet, "http://api.github.com/copilot_internal/v2/token", ""), false},
	} {
		if got := provimuxObserveRequest(tc.req).housekeeping; got != tc.want {
			t.Errorf("%s %s: housekeeping %v, want %v", tc.req.Method, tc.req.URL, got, tc.want)
		}
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
