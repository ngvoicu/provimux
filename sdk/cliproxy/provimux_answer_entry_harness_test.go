package cliproxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"google.golang.org/protobuf/encoding/protowire"
)

// Provimux: the harness of the wire tests of an answer's entry (answered-by.md 8.4, "What was sent (W6a)"). The real
// server (api.NewServer over a Service built from a config, as namesHarness builds it) answers a client over HTTP;
// every credential's requests go to one in-memory stand-in vendor (the manager's per-credential transport, which every
// executor's client uses where no proxy is set), whatever host they name. The stand-in records the host it was called
// on, the credential headers and the body it received, reads the model from them itself, and answers in the vendor's
// form. A test compares the answer's X-Provimux-* headers with what the stand-in recorded, never with what it
// configured. Nothing leaves the process; every key, login and id is invented.

const sentClientKey = "provimux-test-sent-client-key"

// sentReceived is one request as the stand-in received it.
type sentReceived struct {
	Scheme, Host, Port, Path string
	Signer                   string // the credential headers by name: x-api-key, authorization, both, ""
	Model                    string // the model as the stand-in reads it from what it received
	Status                   int    // what it answered
}

// sentStandIn is the stand-in vendor: one transport for every credential.
type sentStandIn struct {
	t        *testing.T
	mu       sync.Mutex
	received []sentReceived
	// status answers a request with a status other than 200 when it returns one (a refusal, a redirect).
	status func(r *http.Request, n int) (int, http.Header)
}

func (s *sentStandIn) RoundTripperFor(*coreauth.Auth) http.RoundTripper { return s }

func (s *sentStandIn) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.received, s.status = nil, nil
}

// answered returns the received requests that carried a model and were answered with a 2xx.
func (s *sentStandIn) answered() []sentReceived {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []sentReceived
	for _, r := range s.received {
		if r.Model != "" && r.Status >= 200 && r.Status < 300 {
			out = append(out, r)
		}
	}
	return out
}

func (s *sentStandIn) all() []sentReceived {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sentReceived(nil), s.received...)
}

// standInModel reads the model of a received request the stand-in's own way: Devin's chat message through upstream's
// own projection of its business fields (helps.FinalizeDevinPayload); the id in a .../models/<id>:<method> path; the
// top-level "model" of a JSON body (zstd decoded where it says so).
func standInModel(t *testing.T, r *http.Request, body []byte) string {
	if strings.HasSuffix(r.URL.Path, helps.DevinChatPath) {
		if len(body) < 5 {
			t.Errorf("stand-in: a Devin chat request without a frame")
			return ""
		}
		_, view, err := helps.FinalizeDevinPayload(body[5:], nil)
		if err != nil {
			t.Errorf("stand-in: Devin's projection: %v", err)
			return ""
		}
		return ojson.GetBytes(view, "model").String()
	}
	if i := strings.LastIndex(r.URL.Path, "/models/"); i >= 0 {
		if rest := r.URL.Path[i+len("/models/"):]; strings.Contains(rest, ":") {
			return rest[:strings.Index(rest, ":")]
		}
	}
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "zstd") {
		decoder, _ := zstd.NewReader(nil)
		decoded, err := decoder.DecodeAll(body, nil)
		decoder.Close()
		if err != nil {
			t.Errorf("stand-in: a zstd body it cannot decode: %v", err)
			return ""
		}
		body = decoded
	}
	if len(body) > 0 && body[0] == '{' {
		return ojson.GetBytes(body, "model").String()
	}
	return ""
}

func standInSigner(header http.Header) string {
	var key, authorization bool
	for name := range header {
		key = key || strings.EqualFold(name, "x-api-key")
		authorization = authorization || strings.EqualFold(name, "authorization")
	}
	switch {
	case key && authorization:
		return "both"
	case key:
		return "x-api-key"
	case authorization:
		return "authorization"
	}
	return ""
}

func (s *sentStandIn) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
	}
	received := sentReceived{Scheme: r.URL.Scheme, Host: r.URL.Hostname(), Port: r.URL.Port(), Path: r.URL.Path, Signer: standInSigner(r.Header), Model: standInModel(s.t, r, body)}
	s.mu.Lock()
	n := len(s.received)
	status, header := http.StatusOK, http.Header{}
	if s.status != nil {
		if code, extra := s.status(r, n); code != 0 {
			status = code
			for k, v := range extra {
				header[k] = v
			}
		}
	}
	received.Status = status
	s.received = append(s.received, received)
	s.mu.Unlock()
	if status != http.StatusOK {
		if header.Get("Content-Type") == "" {
			header.Set("Content-Type", "application/json")
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"provimux-test stand-in refusal","type":"invalid_request_error"}}`)), Request: r}, nil
	}
	return standInAnswer(r, body, received.Model), nil
}

// standInAnswer is the vendor's answer, in the form of its protocol.
func standInAnswer(r *http.Request, body []byte, model string) *http.Response {
	reply := func(contentType, text string) *http.Response {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(text)), Request: r}
	}
	stream := ojson.GetBytes(body, "stream").Bool()
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, helps.DevinChatPath):
		var frame []byte
		frame = protowire.AppendTag(frame, 1, protowire.BytesType)
		frame = protowire.AppendString(frame, "provimux-test-bot")
		frame = protowire.AppendTag(frame, 3, protowire.BytesType)
		frame = protowire.AppendString(frame, "provimux-test-answer")
		var out bytes.Buffer
		out.Write(helps.WrapConnectEnvelope(frame))
		out.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))
		return reply("application/connect+proto", out.String())
	case strings.HasSuffix(path, "/messages/count_tokens"):
		return reply("application/json", `{"input_tokens":3}`)
	case strings.HasSuffix(path, "/messages"):
		message := fmt.Sprintf(`{"id":"msg_provimux_test","type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"provimux-test-answer"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`, model)
		if !stream {
			return reply("application/json", message)
		}
		return reply("text/event-stream", "event: message_start\ndata: "+fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_provimux_test","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}`, model)+"\n\n"+
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"provimux-test-answer\"}}\n\n"+
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n"+
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	case strings.HasSuffix(path, "/responses") || strings.HasSuffix(path, "/responses/compact"):
		completed := fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_provimux_test","object":"response","created_at":1,"status":"completed","model":%q,"output":[{"id":"msg_provimux_test","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"provimux-test-answer","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`, model)
		created := fmt.Sprintf(`{"type":"response.created","response":{"id":"resp_provimux_test","object":"response","created_at":1,"status":"in_progress","model":%q,"output":[]}}`, model)
		return reply("text/event-stream", "event: response.created\ndata: "+created+"\n\nevent: response.completed\ndata: "+completed+"\n\n")
	case strings.Contains(path, ":countTokens"):
		return reply("application/json", `{"totalTokens":3}`)
	case strings.Contains(path, ":streamGenerateContent") || strings.Contains(path, ":generateContent"):
		candidate := fmt.Sprintf(`{"candidates":[{"content":{"role":"model","parts":[{"text":"provimux-test-answer"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2},"modelVersion":%q}`, model)
		if strings.HasPrefix(path, "/v1internal") {
			candidate = `{"response":` + candidate + `}`
		}
		if strings.Contains(path, ":streamGenerateContent") {
			return reply("text/event-stream", "data: "+candidate+"\n\n")
		}
		return reply("application/json", candidate)
	case strings.HasSuffix(path, "/chat/completions"):
		if stream {
			return reply("text/event-stream", "data: "+fmt.Sprintf(`{"id":"chatcmpl-provimux-test","object":"chat.completion.chunk","created":1,"model":%q,"choices":[{"index":0,"delta":{"role":"assistant","content":"provimux-test-answer"},"finish_reason":null}]}`, model)+"\n\n"+
				"data: "+fmt.Sprintf(`{"id":"chatcmpl-provimux-test","object":"chat.completion.chunk","created":1,"model":%q,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, model)+"\n\ndata: [DONE]\n\n")
		}
		return reply("application/json", fmt.Sprintf(`{"id":"chatcmpl-provimux-test","object":"chat.completion","created":1,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"provimux-test-answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, model))
	case strings.HasSuffix(path, "/token"):
		return reply("application/json", `{"access_token":"provimux-test-access","token_type":"Bearer","expires_in":3600}`)
	}
	return reply("application/json", `{}`)
}

// sentHarness is the real server, one stand-in vendor, models.answered-by on and a marker key.
type sentHarness struct {
	*namesHarness
	stand  *sentStandIn
	server *httptest.Server
}

func newSentHarness(t *testing.T, configBody string) *sentHarness {
	t.Helper()
	names := newNamesHarness(t, "access:\n  api-keys:\n    - "+sentClientKey+"\n"+configBody)
	// After the service is built: it puts the config's switch in force, off in a test binary.
	coreauth.SetProvimuxMarkerKey([]byte("provimux-test-sent-marker-key-32"))
	was := logging.SetProvimuxAnsweredBy(true)
	t.Cleanup(func() {
		logging.SetProvimuxAnsweredBy(was)
		coreauth.SetProvimuxMarkerKey(nil)
	})
	stand := &sentStandIn{t: t}
	names.service.coreManager.SetRoundTripperProvider(stand)
	server := httptest.NewServer(names.handler)
	t.Cleanup(server.Close)
	return &sentHarness{namesHarness: names, stand: stand, server: server}
}

// register registers auth with the service's manager, binds its executor and puts models on it.
func (h *sentHarness) register(auth *coreauth.Auth, models ...string) *coreauth.Auth {
	h.t.Helper()
	registered, err := h.service.coreManager.Register(h.t.Context(), auth)
	if err != nil {
		h.t.Fatal(err)
	}
	h.service.registerExecutorForAuth(registered, false)
	if len(models) > 0 {
		infos := make([]*registry.ModelInfo, 0, len(models))
		for _, model := range models {
			infos = append(infos, &registry.ModelInfo{ID: model, Object: "model", OwnedBy: "provimux-test"})
		}
		registry.GetGlobalRegistry().RegisterClient(registered.ID, registered.Provider, infos)
	}
	id := registered.ID
	h.t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	return registered
}

// sentAnswer is the gateway's answer as the client read it.
type sentAnswer struct {
	status int
	header http.Header
	body   string
}

func (a sentAnswer) get(name string) string { return a.header.Get(name) }

// post sends one request to the gateway over HTTP and reads the whole answer.
func (h *sentHarness) post(path, body string, header http.Header) sentAnswer {
	h.t.Helper()
	request, err := http.NewRequest(http.MethodPost, h.server.URL+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+sentClientKey)
	request.Header.Set("Content-Type", "application/json")
	for name, values := range header {
		request.Header[name] = values
	}
	response, err := h.server.Client().Do(request)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	answer, _ := io.ReadAll(response.Body)
	return sentAnswer{status: response.StatusCode, header: response.Header, body: string(answer)}
}

func chatBody(model string, stream bool, extra string) string {
	return fmt.Sprintf(`{"model":%q,"stream":%v,"messages":[{"role":"user","content":"provimux-test question"}]%s}`, model, stream, extra)
}

// checkSent asserts the answer's statement against what the stand-in received: one model request answered, and the
// answer's X-Provimux-Model-Sent is its model; X-Provimux-Entry is wantEntry (none for ""), X-Provimux-Model is the
// sent model with basis not-confirmed, no X-Provimux-Effort, and the marker is account's. It returns what the
// stand-in received.
func (h *sentHarness) checkSent(answer sentAnswer, account *coreauth.Auth, wantHost, wantEntry string) sentReceived {
	h.t.Helper()
	if answer.status != http.StatusOK {
		h.t.Fatalf("the gateway answered %d: %s (stand-in received %+v)", answer.status, answer.body, h.stand.all())
	}
	answered := h.stand.answered()
	if len(answered) == 0 {
		h.t.Fatalf("no model request reached the stand-in: %+v", h.stand.all())
	}
	got := answered[len(answered)-1]
	for _, other := range answered {
		if other.Host != got.Host || other.Model != got.Model || other.Signer != got.Signer {
			h.t.Fatalf("fixture: the attempt sent two answered model requests %+v", answered)
		}
	}
	if wantHost != "" && got.Host != wantHost {
		h.t.Fatalf("fixture: the stand-in was called on %s, want %s (%+v)", got.Host, wantHost, got)
	}
	if marker := account.ProvimuxAccountMarker(); marker == "" || answer.get(logging.ProvimuxAccountHeader) != marker {
		h.t.Fatalf("X-Provimux-Account %q, want %q", answer.get(logging.ProvimuxAccountHeader), marker)
	}
	if sent := answer.get(logging.ProvimuxModelSentHeader); sent != got.Model {
		h.t.Fatalf("X-Provimux-Model-Sent %q, the stand-in received %q (%+v; headers %v)", sent, got.Model, got, answer.header)
	}
	if model, basis := answer.get(logging.ProvimuxModelHeader), answer.get(logging.ProvimuxModelBasisHeader); model != got.Model || basis != "not-confirmed" {
		h.t.Fatalf("X-Provimux-Model %q basis %q, want %q not-confirmed", model, basis, got.Model)
	}
	if values := answer.header.Values(logging.ProvimuxEffortHeader); len(values) != 0 {
		h.t.Fatalf("X-Provimux-Effort written: %v", values)
	}
	if entry := answer.header.Values(logging.ProvimuxEntryHeader); (wantEntry == "" && len(entry) != 0) || (wantEntry != "" && (len(entry) != 1 || entry[0] != wantEntry)) {
		h.t.Fatalf("X-Provimux-Entry %v, want %q (stand-in received %+v)", entry, wantEntry, got)
	}
	return got
}

// checkNoModel asserts an answer that names its account and nothing of what was sent.
func (h *sentHarness) checkNoModel(answer sentAnswer, account *coreauth.Auth) {
	h.t.Helper()
	if answer.status != http.StatusOK {
		h.t.Fatalf("the gateway answered %d: %s (stand-in received %+v)", answer.status, answer.body, h.stand.all())
	}
	if marker := account.ProvimuxAccountMarker(); marker == "" || answer.get(logging.ProvimuxAccountHeader) != marker {
		h.t.Fatalf("X-Provimux-Account %q, want %q", answer.get(logging.ProvimuxAccountHeader), marker)
	}
	for _, name := range logging.ProvimuxFieldNames()[1:] {
		if values := answer.header.Values(name.Header); len(values) != 0 {
			h.t.Fatalf("%s written: %v (stand-in received %+v)", name.Header, values, h.stand.all())
		}
	}
}
