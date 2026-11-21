package pluginhost

// A stream that passes through the stream-chunk interceptor chain names nobody, whatever the chain does with its chunks
// (DESIGN.md 12.1, after the third review). The native Codex executor answers two Codex keys from loopback upstreams:
// the outer key at 127.0.0.1 answers resp-outer, the inner key at localhost resp-inner. A stream-chunk interceptor in
// the production plugin host sees the outer stream; a standard client reads the stock /v1/responses route over SSE and
// over a websocket. Derived from the third review's probes (/tmp/provimux-t280.av43U3xu/probes/labelled_test.go,
// visible_test.go).
//
// Modes:
//
//	labelled-replace     the upstream sends "event: response.created" as a chunk of its own; the interceptor passes the
//	                     label line and replaces the creation and completion payloads with the inner key's
//	metadata-replace     the upstream opens with a response.metadata event, which the route filters out; the interceptor
//	                     passes it and replaces the creation and completion payloads with the inner key's
//	watch                the upstream opens with response.metadata; the interceptor returns every chunk as given
//	no-interceptor       the plugin host has no plugin with the capability: the stream keeps the outer key's name

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	a "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	chunkBytesModel    = "gpt-5.4-mini"
	chunkBytesPluginID = "chunk-bytes"
)

func chunkBytesData(chunk []byte) []byte {
	for _, line := range bytes.Split(chunk, []byte("\n")) {
		if raw, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:")); ok {
			return bytes.TrimSpace(raw)
		}
	}
	return bytes.TrimSpace(chunk)
}

// chunkBytesInterceptor replaces the outer stream's creation and completion payloads with the inner key's answer, or,
// in watch mode, returns every chunk as given.
type chunkBytesInterceptor struct {
	base     *handlers.BaseAPIHandler
	mode     string
	mu       sync.Mutex
	inner    map[string][]byte
	seen     atomic.Int32
	label    atomic.Int32
	metadata atomic.Int32
	replaced atomic.Int32
}

func (c *chunkBytesInterceptor) InterceptStreamChunk(ctx context.Context, r pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error) {
	if r.ChunkIndex < 0 {
		return pluginapi.StreamChunkInterceptResponse{}, nil
	}
	c.seen.Add(1)
	if strings.TrimSpace(string(r.Body)) == "event: response.created" {
		c.label.Add(1)
	}
	typ := ojson.GetBytesNoCopy(chunkBytesData(r.Body), "type").String()
	if typ == "response.metadata" {
		c.metadata.Add(1)
	}
	if c.mode == "watch" || (typ != "response.created" && typ != "response.completed") {
		return pluginapi.StreamChunkInterceptResponse{Body: r.Body}, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inner == nil {
		res, errMsg := c.base.ExecuteModelStream(ctx, handlers.ModelExecutionRequest{
			EntryProtocol: "openai-response", Model: chunkBytesModel, AuthID: "chunk-inner", Stream: true, SkipInterceptorPluginID: chunkBytesPluginID,
			Body: []byte(fmt.Sprintf(`{"model":%q,"input":"inner","stream":true}`, chunkBytesModel)),
		})
		if errMsg != nil {
			return pluginapi.StreamChunkInterceptResponse{}, errMsg.Error
		}
		c.inner = map[string][]byte{}
		for piece := range res.Chunks {
			if piece.Err != nil {
				return pluginapi.StreamChunkInterceptResponse{}, piece.Err
			}
			if k := ojson.GetBytesNoCopy(chunkBytesData(piece.Payload), "type").String(); k != "" {
				c.inner[k] = bytes.Clone(piece.Payload)
			}
		}
	}
	if len(c.inner[typ]) == 0 {
		return pluginapi.StreamChunkInterceptResponse{}, fmt.Errorf("fixture: no inner %s", typ)
	}
	c.replaced.Add(1)
	return pluginapi.StreamChunkInterceptResponse{Body: c.inner[typ]}, nil
}

// A stream that passes through a stream-chunk interceptor names nobody on SSE and on the websocket's response.created,
// also when the interceptor only watches; a stream with no interceptor in its path keeps its name. (DESIGN.md 12.4, after
// the third review.)
func TestStreamThroughAChunkInterceptorNamesNobody(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	outerStatement := [3]string{"apikey", "codex", "127.0.0.1"}
	for _, mode := range []string{"labelled-replace", "metadata-replace", "watch", "no-interceptor"} {
		for _, via := range []string{"sse", "ws"} {
			t.Run(mode+" "+via, func(t *testing.T) {
				var outerCalls, innerCalls atomic.Int32
				upstream := func(id string, calls *atomic.Int32) *httptest.Server {
					s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						calls.Add(1)
						w.Header().Set("Content-Type", "text/event-stream")
						flusher, _ := w.(http.Flusher)
						write := func(s string) {
							_, _ = io.WriteString(w, s)
							if flusher != nil {
								flusher.Flush()
							}
						}
						if id == "outer" && mode != "labelled-replace" {
							write("data: {\"type\":\"response.metadata\",\"metadata\":{\"fixture\":\"private-prelude\"}}\n\n")
						}
						if id == "outer" && mode == "labelled-replace" {
							write("event: response.created\n")
						}
						write(fmt.Sprintf("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-%s\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\n", id))
						write(fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-%s\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n", id))
					}))
					t.Cleanup(s.Close)
					return s
				}
				outer := upstream("outer", &outerCalls)
				inner := upstream("inner", &innerCalls)
				m := a.NewManager(nil, &a.FillFirstSelector{}, nil)
				m.RegisterExecutor(runtimeexecutor.NewCodexExecutor(&config.Config{}))
				for _, x := range []struct{ id, base, priority string }{
					{"chunk-outer", outer.URL, "10"},
					{"chunk-inner", strings.Replace(inner.URL, "127.0.0.1", "localhost", 1), "0"},
				} {
					cred := &a.Auth{ID: x.id, Provider: "codex", Status: a.StatusActive, Attributes: map[string]string{"auth_kind": "apikey", "base_url": x.base, "api_key": "fixture-key", "priority": x.priority}}
					if _, err := m.Register(context.Background(), cred); err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(cred.ID, "codex", []*registry.ModelInfo{{ID: chunkBytesModel}})
					id := cred.ID
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				}
				base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, m)
				interceptor := &chunkBytesInterceptor{base: base, mode: mode}
				host := newHostWithRecords()
				if mode != "no-interceptor" {
					host = newHostWithRecords(capabilityRecord{id: chunkBytesPluginID, plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{StreamChunkInterceptor: interceptor}}})
				}
				base.SetPluginHost(host)
				engine := gin.New()
				engine.Use(logging.CPATraceIDMiddleware())
				handler := openai.NewOpenAIResponsesAPIHandler(base)
				engine.POST("/v1/responses", handler.Responses)
				engine.GET("/v1/responses", handler.ResponsesWebsocket)
				server := httptest.NewServer(engine)
				t.Cleanup(server.Close)
				url := server.URL + "/v1/responses"
				request := fmt.Sprintf(`{"model":%q,"input":[{"type":"message","role":"user","content":"hi"}],"stream":true}`, chunkBytesModel)

				var visible [][]byte
				var fields [3]string
				var dump bytes.Buffer
				if via == "sse" {
					req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(request))
					if err != nil {
						t.Fatal(err)
					}
					req.Header.Set("Content-Type", "application/json")
					res, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = res.Body.Close() }()
					body, err := io.ReadAll(res.Body)
					if err != nil {
						t.Fatal(err)
					}
					fields = [3]string{res.Header.Get(logging.CPAAuthKindHeader), res.Header.Get(logging.CPAAuthProviderHeader), res.Header.Get(logging.CPAAuthHostHeader)}
					fmt.Fprintf(&dump, "status=%d\n%s", res.StatusCode, body)
					for _, line := range bytes.Split(body, []byte("\n")) {
						if val, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:")); ok {
							visible = append(visible, bytes.Clone(bytes.TrimSpace(val)))
						}
					}
				} else {
					conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(url, "http"), nil)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = conn.Close() }()
					if err := conn.WriteMessage(websocket.TextMessage, []byte(strings.Replace(request, "{", `{"type":"response.create",`, 1))); err != nil {
						t.Fatal(err)
					}
					_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
					for done := false; !done; {
						_, frame, err := conn.ReadMessage()
						if err != nil {
							t.Fatalf("read: %v (frames so far %s)", err, dump.Bytes())
						}
						fmt.Fprintf(&dump, "%s\n", frame)
						visible = append(visible, frame)
						frameFields := [3]string{ojson.GetBytesNoCopy(frame, "cpa_auth_kind").String(), ojson.GetBytesNoCopy(frame, "cpa_auth_provider").String(), ojson.GetBytesNoCopy(frame, "cpa_auth_host").String()}
						switch ojson.GetBytesNoCopy(frame, "type").String() {
						case "response.created":
							fields = frameFields
						case "response.completed", "error":
							done = true
							fallthrough
						default:
							if frameFields != ([3]string{}) {
								t.Fatalf("a frame other than response.created carries %q: %s", frameFields, frame)
							}
						}
					}
				}
				var events, ids []string
				for _, v := range visible {
					events = append(events, ojson.GetBytesNoCopy(v, "type").String())
					ids = append(ids, ojson.GetBytesNoCopy(v, "response.id").String())
				}
				replaces := mode == "labelled-replace" || mode == "metadata-replace"
				wantIDs := "[resp-outer resp-outer]"
				if replaces {
					wantIDs = "[resp-inner resp-inner]"
				}
				switch {
				case fmt.Sprint(events) != "[response.created response.completed]" || fmt.Sprint(ids) != wantIDs:
					t.Fatalf("fixture: client events %q ids %q, want ids %s: %s", events, ids, wantIDs, dump.Bytes())
				case outerCalls.Load() != 1 || (innerCalls.Load() == 1) != replaces:
					t.Fatalf("fixture: outer calls %d, inner calls %d", outerCalls.Load(), innerCalls.Load())
				case mode == "labelled-replace" && (interceptor.label.Load() != 1 || interceptor.replaced.Load() != 2):
					t.Fatalf("fixture: label lines seen %d, payloads replaced %d", interceptor.label.Load(), interceptor.replaced.Load())
				case mode == "metadata-replace" && (interceptor.metadata.Load() != 1 || interceptor.replaced.Load() != 2):
					t.Fatalf("fixture: metadata events seen %d, payloads replaced %d", interceptor.metadata.Load(), interceptor.replaced.Load())
				case mode == "watch" && (interceptor.seen.Load() < 3 || interceptor.replaced.Load() != 0):
					t.Fatalf("fixture: chunks seen %d, replaced %d", interceptor.seen.Load(), interceptor.replaced.Load())
				case mode == "no-interceptor" && interceptor.seen.Load() != 0:
					t.Fatalf("fixture: the interceptor saw %d chunks", interceptor.seen.Load())
				}
				want := [3]string{}
				if mode == "no-interceptor" {
					want = outerStatement
				}
				if fields != want {
					t.Fatalf("first client payload %s carries %q, want %q", ids[0], fields, want)
				}
			})
		}
	}
}
