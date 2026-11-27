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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/origin"
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

// sentPublic is what a test compares of an observation: its exported values.
type sentPublic struct {
	Scheme, Host, Port, Model, Signer string
	Status                            int
}

func public(r ProvimuxSentRequest) sentPublic {
	return sentPublic{Scheme: r.Scheme, Host: r.Host, Port: r.Port, Model: r.Model, Signer: r.Signer, Status: r.Status}
}

// provimuxReadModel observes req as the decorator does, lets a transport read its body as Go's does (to its end), and
// reads the model from what it read.
func provimuxReadModel(req *http.Request) (model string, unread bool) {
	observed := provimuxObserveRequest(req)
	sent, capture := provimuxCaptureBody(req)
	if sent.Body != nil {
		_, _ = io.Copy(io.Discard, sent.Body)
		_ = sent.Body.Close()
	}
	observed.body = capture
	return provimuxModelOf(observed)
}

// observedBody is an observation whose transport read body whole ("" = no body).
func observedBody(host, method, path, mediaType, body string, status int) ProvimuxSentRequest {
	capture := &provimuxBodyCapture{none: body == ""}
	if body != "" {
		stream := &provimuxBodyStream{whole: true}
		stream.data.WriteString(body)
		capture.streams = append(capture.streams, stream)
	}
	return ProvimuxSentRequest{Scheme: "https", Host: host, Signer: "authorization", Status: status, method: method, path: path, mediaType: mediaType, body: capture}
}

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
		{"a body without GetBody: read as the transport reads it", func(t *testing.T) *http.Request {
			req := jsonRequest(t, "https://api.anthropic.com/v1/messages", `{"model":"provimux-test-a"}`)
			req.Body = io.NopCloser(strings.NewReader(`{"model":"provimux-test-a"}`))
			req.GetBody = nil
			return req
		}, "provimux-test-a", false},
		{"a body whose GetBody replays other bytes: the bytes the transport read", func(t *testing.T) *http.Request {
			req := jsonRequest(t, "https://openrouter.ai/api/v1/chat/completions", `{"model":"lab/first"}`)
			req.Body = io.NopCloser(strings.NewReader(`{"model":"lab/other"}`))
			req.ContentLength = int64(len(`{"model":"lab/other"}`))
			return req
		}, "lab/other", false},
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
		})
	}
}

// The decorator reads and never consumes: the stand-in receives every byte, and the request's body is the one the
// client handed over.
func TestProvimuxSentDecoratorChangesNothing(t *testing.T) {
	provimuxSentOn(t, true)
	const body = `{"model":"provimux-test-a","messages":[]}`
	ctx := WithUpstreamAttemptTracker(WithProvimuxStatementAsked(context.Background()))
	stand := &standIn{status: http.StatusOK}
	send(t, ctx, stand, jsonRequest(t, "https://api.anthropic.com/v1/messages", body))
	if len(stand.received) != 1 || string(stand.received[0]) != body {
		t.Fatalf("the stand-in received %q, want %q", stand.received, body)
	}
	sent, ok := ProvimuxSent(ctx, "claude")
	want := ProvimuxSentRequest{Scheme: "https", Host: "api.anthropic.com", Model: "provimux-test-a", Status: http.StatusOK}
	if !ok || public(sent) != public(want) {
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
	ctx := WithUpstreamAttemptTracker(WithProvimuxStatementAsked(context.Background()))
	client := &http.Client{Transport: &standIn{status: http.StatusOK}}
	if httptransport.Decorate(ctx, client) != client {
		t.Fatal("a decorator is attached with the switch off")
	}
	send(t, ctx, client.Transport, jsonRequest(t, "https://api.anthropic.com/v1/messages", `{"model":"provimux-test-a"}`))
	if _, ok := ProvimuxSent(ctx, "claude"); ok {
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

// A decorator another program put on the context stays, inside ours, and sees each request once; the attempt names
// nothing, whatever the decorator says of itself (the lead's amendment of 2026-11-27 (2), rule A.2: no recognition by
// declaration). An attempt inside another (a refresh) notes its requests on its own log, not on the enclosing one's,
// and an enclosing attempt's decorator is not another program's.
func TestProvimuxSentKeepsAnEmbeddersDecoratorAndNests(t *testing.T) {
	provimuxSentOn(t, true)
	for _, embedder := range []bool{false, true} {
		var seen atomic.Int32
		base := WithProvimuxStatementAsked(context.Background())
		if embedder {
			base = httptransport.WithDecorator(base, func(next http.RoundTripper) http.RoundTripper {
				return recordingDecorator{next: next, seen: &seen}
			})
		}
		first := WithUpstreamAttemptTracker(base)
		second := WithUpstreamAttemptTracker(first)
		stand := &standIn{status: http.StatusOK}
		send(t, second, stand, jsonRequest(t, "https://api.anthropic.com/v1/messages", `{"model":"provimux-test-a"}`))
		if embedder && seen.Load() != 1 {
			t.Fatalf("the embedder's decorator saw %d requests, want 1", seen.Load())
		}
		if log := provimuxSentLogOf(first); len(log.requests) != 0 {
			t.Fatalf("a request of the inner attempt landed on the outer: %+v", log.requests)
		}
		log := provimuxSentLogOf(second)
		sent, ok := ProvimuxSent(second, "claude")
		if len(log.requests) != 1 || ok == embedder || (ok && sent.Model != "provimux-test-a") {
			t.Fatalf("embedder %v: %+v %v, %d observations", embedder, sent, ok, len(log.requests))
		}
	}
}

// The rule (the lead's of 2026-11-27): exactly one request that could have produced the answer, read whole, nothing
// making it not wholly the gateway's own. The answering executor's housekeeping and refusals do not count; everything
// else does.
func TestProvimuxSentTheRule(t *testing.T) {
	answered := observedBody("api.x.ai", http.MethodPost, "/v1/responses", "application/json", `{"model":"provimux-test-a"}`, 200)
	with := func(change func(*ProvimuxSentRequest)) ProvimuxSentRequest {
		r := answered
		change(&r)
		return r
	}
	token := observedBody("oauth2.googleapis.com", http.MethodPost, "/token", "application/x-www-form-urlencoded", "grant_type=provimux-test", 200)
	for _, tc := range []struct {
		name     string
		executor string
		requests []ProvimuxSentRequest
		overflow bool
		foreign  bool
		want     bool
	}{
		{"none", "xai", nil, false, false, false},
		{"one answered model request", "xai", []ProvimuxSentRequest{answered}, false, false, true},
		{"a refusal, then the same again answered", "xai", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.Status = 400 }), answered}, false, false, true},
		{"a redirect, then the host that answered", "xai", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.Status = 307; r.Host = "provimux-test.example" }), answered}, false, false, true},
		{"a transport error, then the same answered", "xai", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.Status = 0 }), answered}, false, false, true},
		{"the answering executor's housekeeping beside it", "vertex", []ProvimuxSentRequest{token, answered}, false, false, true},
		{"another executor's housekeeping beside it", "xai", []ProvimuxSentRequest{token, answered}, false, false, false},
		{"an answered request without a model, not housekeeping", "xai", []ProvimuxSentRequest{observedBody("auth.example.test", http.MethodPost, "/token", "application/x-www-form-urlencoded", "a=b", 200), answered}, false, false, false},
		{"the same answered twice", "xai", []ProvimuxSentRequest{answered, answered}, false, false, false},
		{"two answered requests, two models", "xai", []ProvimuxSentRequest{answered, observedBody("api.x.ai", http.MethodPost, "/v1/responses", "application/json", `{"model":"provimux-test-b"}`, 200)}, false, false, false},
		{"an answered request it could not read", "xai", []ProvimuxSentRequest{answered, with(func(r *ProvimuxSentRequest) { r.unread = true })}, false, false, false},
		{"the one request unread", "xai", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.unread = true })}, false, false, false},
		{"the one request's body not read whole", "xai", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.body = &provimuxBodyCapture{streams: []*provimuxBodyStream{{}}} })}, false, false, false},
		{"the one request untrusted", "xai", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.untrusted = true })}, false, false, false},
		{"the one request without a model", "xai", []ProvimuxSentRequest{observedBody("api.x.ai", http.MethodPost, "/v1/responses", "application/json", `{"input":[]}`, 200)}, false, false, false},
		{"a refused request it could not read", "xai", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.unread = true; r.Status = 400 }), answered}, false, false, true},
		{"only refusals", "xai", []ProvimuxSentRequest{with(func(r *ProvimuxSentRequest) { r.Status = 500 })}, false, false, false},
		{"more than the log keeps", "xai", []ProvimuxSentRequest{answered}, true, false, false},
		{"a connection the decorator did not see", "xai", []ProvimuxSentRequest{answered}, false, true, false},
		{"no executor kind", "", []ProvimuxSentRequest{answered}, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := &provimuxSentLog{}
			ctx := context.WithValue(context.Background(), provimuxSentLogKey{}, log)
			for _, request := range tc.requests {
				// Each case's observation has a copy of its own: ProvimuxSent lets the copies go once it read them.
				if request.body != nil {
					copied := &provimuxBodyCapture{none: request.body.none}
					for _, stream := range request.body.streams {
						fresh := &provimuxBodyStream{whole: stream.whole, failed: stream.failed}
						fresh.data.Write(stream.data.Bytes())
						copied.streams = append(copied.streams, fresh)
					}
					request.body = copied
				}
				log.add(request)
			}
			log.overflow = log.overflow || tc.overflow
			if tc.foreign {
				log.conns.Add(1)
			}
			sent, ok := ProvimuxSent(ctx, tc.executor)
			want := public(answered)
			want.Model = "provimux-test-a"
			if ok != tc.want || (ok && public(sent) != want) {
				t.Fatalf("ProvimuxSent = %+v %v, want %v", sent, ok, tc.want)
			}
		})
	}
	if _, ok := ProvimuxSent(context.Background(), "xai"); ok {
		t.Fatal("a context without a log names a model")
	}
}

// A request on another context than the attempt's (detached) is noted on the attempt as untrusted: the attempt
// names nothing.
func TestProvimuxSentDetachedRequestCancels(t *testing.T) {
	provimuxSentOn(t, true)
	ctx := WithUpstreamAttemptTracker(WithProvimuxStatementAsked(context.Background()))
	stand := &standIn{status: http.StatusOK}
	client := httptransport.Decorate(ctx, &http.Client{Transport: stand})
	for _, requestCtx := range []context.Context{ctx, context.Background()} {
		req := jsonRequest(t, "https://api.x.ai/v1/responses", `{"model":"provimux-test-a"}`)
		resp, err := client.Do(req.WithContext(requestCtx))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		_, ok := ProvimuxSent(ctx, "xai")
		if want := requestCtx == ctx; ok != want {
			t.Fatalf("after a request on the attempt's context %v: named %v", requestCtx == ctx, ok)
		}
	}
}

// What leaves is wholly the gateway's own: a Host other than the URL's, a per-credential transport on the context that
// the gateway's own provider did not build (recognised by identity, never by its Go package or a declaration), and Go's
// default transport replaced, give nothing (the lead's amendment of 2026-11-27 (2), rule A.2). The transport on the
// client is the one the gateway's helper chose from those parts.
func TestProvimuxSentWhatLeaves(t *testing.T) {
	provimuxSentOn(t, true)
	type plain struct{ http.RoundTripper }
	gatewayMade := &standIn{status: http.StatusOK}
	origin.StandInForTests(gatewayMade)
	for _, tc := range []struct {
		name    string
		req     func(*http.Request)
		ctx     func(context.Context, *standIn) context.Context
		replace bool
		want    bool
	}{
		{name: "the URL's host", want: true},
		{name: "the URL's host as Host, with the default port", req: func(r *http.Request) { r.Host = "API.x.ai:443" }, want: true},
		{name: "another Host", req: func(r *http.Request) { r.Host = "routing.provimux-test.invalid" }},
		{name: "another port in Host", req: func(r *http.Request) { r.Host = "api.x.ai:8443" }},
		{name: "a context transport the gateway did not build", ctx: func(c context.Context, stand *standIn) context.Context {
			return context.WithValue(c, "cliproxy.roundtripper", http.RoundTripper(plain{stand}))
		}},
		{name: "a Go transport the gateway did not build, as the context transport", ctx: func(c context.Context, _ *standIn) context.Context {
			return context.WithValue(c, "cliproxy.roundtripper", http.RoundTripper(&http.Transport{}))
		}},
		{name: "a context transport the gateway built", ctx: func(c context.Context, _ *standIn) context.Context {
			return context.WithValue(c, "cliproxy.roundtripper", http.RoundTripper(gatewayMade))
		}, want: true},
		{name: "Go's default transport replaced", replace: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.replace {
				was := http.DefaultTransport
				http.DefaultTransport = &http.Transport{}
				t.Cleanup(func() { http.DefaultTransport = was })
			}
			ctx := WithUpstreamAttemptTracker(WithProvimuxStatementAsked(context.Background()))
			stand := &standIn{status: http.StatusOK}
			if tc.ctx != nil {
				ctx = tc.ctx(ctx, stand)
			}
			req := jsonRequest(t, "https://api.x.ai/v1/responses", `{"model":"provimux-test-a"}`)
			if tc.req != nil {
				tc.req(req)
			}
			log := provimuxSentLogOf(ctx)
			observer := &provimuxSentObserver{next: stand, log: log}
			resp, err := observer.RoundTrip(req.WithContext(ctx))
			if err == nil {
				_ = resp.Body.Close()
			}
			if len(log.requests) != 1 {
				t.Fatalf("%d observations", len(log.requests))
			}
			if _, ok := ProvimuxSent(ctx, "xai"); ok != tc.want {
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
		ctx := WithUpstreamAttemptTracker(WithProvimuxStatementAsked(context.Background()))
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
		if _, ok := ProvimuxSent(ctx, "openai-compatibility"); ok == unseen || log.conns.Load() == 0 {
			t.Fatalf("unseen %v: named %v (connections %d, seen %d)", unseen, ok, log.conns.Load(), log.observed.Load())
		}
	}
}

// The closed housekeeping list, by the executor that answered, exact authority, exact path and shape (the lead's
// amendment of 2026-11-27 (2), rule A.5): a token form to Google's endpoint for the Google executors, Antigravity's
// discovery as JSON, Copilot's token and catalog. The same request for another executor, at a lookalike path, to a
// host given by suffix, with a Host of its own, in another shape or with a model, is a candidate.
func TestProvimuxSentHousekeeping(t *testing.T) {
	form := func(method, url, body string) *http.Request {
		req, _ := http.NewRequest(method, url, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return req
	}
	hosted := func(req *http.Request, host string) *http.Request { req.Host = host; return req }
	for _, tc := range []struct {
		executor string
		req      *http.Request
		want     bool
	}{
		{"vertex", form(http.MethodPost, "https://oauth2.googleapis.com/token", "grant_type=provimux-test"), true},
		{"antigravity", form(http.MethodPost, "https://oauth2.googleapis.com/token", "grant_type=provimux-test"), true},
		{"openai-compatibility", form(http.MethodPost, "https://oauth2.googleapis.com/token", "grant_type=provimux-test"), false},
		{"vertex", hosted(form(http.MethodPost, "https://oauth2.googleapis.com/token", "content=answer"), "routing.provimux-test.invalid"), false},
		{"copilot", form(http.MethodGet, "https://api.github.com/copilot_internal/v2/token", ""), true},
		{"copilot", form(http.MethodGet, "https://api.individual.githubcopilot.com/models", ""), true},
		{"copilot", form(http.MethodGet, "https://api.githubcopilot.com/models", ""), true},
		{"openai-compatibility", form(http.MethodGet, "https://api.githubcopilot.com/models", ""), false},
		{"copilot", form(http.MethodGet, "https://lookalike.githubcopilot.com/models", ""), false},
		{"antigravity", jsonRequest(t, "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist", `{"metadata":{}}`), true},
		{"antigravity", jsonRequest(t, "https://daily-cloudcode-pa.googleapis.com/v1internal:onboardUser", `{"tierId":"x"}`), true},
		{"antigravity", form(http.MethodPost, "https://cloudcode-pa.googleapis.com/tenant/v1internal:loadCodeAssist", "content=answer"), false},
		{"antigravity", form(http.MethodPost, "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist", "content=answer"), false},
		{"antigravity", jsonRequest(t, "https://cloudcode-pa.googleapis.com/v1internal:generateContent", `{"request":{}}`), false},
		{"vertex", jsonRequest(t, "https://oauth2.googleapis.com/token", `{"grant_type":"x"}`), false},
		{"vertex", form(http.MethodPost, "https://oauth2.googleapis.com/token", "model=provimux-test-a"), false},
		{"copilot", form(http.MethodGet, "https://api.githubcopilot.com/models/provimux-test-a", ""), false},
		{"copilot", form(http.MethodGet, "http://api.github.com/copilot_internal/v2/token", ""), false},
	} {
		observed := provimuxObserveRequest(tc.req)
		sent, capture := provimuxCaptureBody(tc.req)
		if sent.Body != nil {
			_, _ = io.Copy(io.Discard, sent.Body)
		}
		observed.body = capture
		observed.Model, observed.unread = provimuxModelOf(observed)
		if got := provimuxHousekeeping(tc.executor, observed); got != tc.want {
			t.Errorf("%s: %s %s (Host %q): housekeeping %v, want %v", tc.executor, tc.req.Method, tc.req.URL, tc.req.Host, got, tc.want)
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
		if got := public(provimuxObserveRequest(req)); got != public(tc.want) {
			t.Errorf("%s %v: %+v, want %+v", tc.url, tc.header, got, tc.want)
		}
	}
}

// The body is copied only for a call whose caller asked for the statement, and the copy goes once the model is read
// (the lead's amendment of 2026-11-27 (2), part 3).
func TestProvimuxSentCopiesOnlyWhenAskedAndLetsGo(t *testing.T) {
	provimuxSentOn(t, true)
	for _, asked := range []bool{false, true} {
		base := context.Background()
		if asked {
			base = WithProvimuxStatementAsked(base)
		}
		ctx := WithUpstreamAttemptTracker(base)
		stand := &standIn{status: http.StatusOK}
		send(t, ctx, stand, jsonRequest(t, "https://api.x.ai/v1/responses", `{"model":"provimux-test-a"}`))
		log := provimuxSentLogOf(ctx)
		if len(log.requests) != 1 || (log.requests[0].body != nil) != asked {
			t.Fatalf("asked %v: a copy %v", asked, log.requests[0].body != nil)
		}
		sent, ok := ProvimuxSent(ctx, "xai")
		if ok != asked || (ok && sent.Model != "provimux-test-a") {
			t.Fatalf("asked %v: %+v %v", asked, sent, ok)
		}
		if asked {
			capture := log.requests[0].body
			capture.mu.Lock()
			kept := 0
			for _, stream := range capture.streams {
				kept += stream.data.Cap()
			}
			capture.mu.Unlock()
			if !capture.released || kept != 0 {
				t.Fatalf("the copy was kept after the model was read: %d bytes", kept)
			}
		}
	}
	if WithProvimuxStatementAsked(context.Background()) == context.Background() {
		t.Fatal("fixture: the switch is on")
	}
}
