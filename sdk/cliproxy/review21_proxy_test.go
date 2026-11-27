package cliproxy

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	ex "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
)

type reviewFrozenRequest struct {
	request *http.Request
	body    []byte
}

func (s reviewFrozenRequest) checkSwitch(t *testing.T) {
	t.Helper()
	saved := logging.ProvimuxAnsweredBy()
	defer logging.SetProvimuxAnsweredBy(saved)
	var encoded [2][]byte
	for i, enable := range []bool{false, true} {
		logging.SetProvimuxAnsweredBy(enable)
		c := ex.WithUpstreamAttemptTracker(context.Background())
		r := s.request.Clone(c)
		open := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(s.body)), nil }
		r.Body, _ = open()
		r.GetBody = open
		rec := reviewRT(func(req *http.Request) (*http.Response, error) {
			var b bytes.Buffer
			if err := req.Write(&b); err != nil {
				return nil, err
			}
			encoded[i] = b.Bytes()
			return reviewOK(req, "{}", "application/json"), nil
		})
		resp, err := httptransport.Decorate(c, &http.Client{Transport: rec}).Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if len(encoded[0]) == 0 || !bytes.Equal(encoded[0], encoded[1]) {
		t.Fatal("outgoing request differs at the receiving transport")
	}
}

// An SDK embedder may put a request translator below the gateway's observer.
// Exercise a native executor and a real TLS server with a synthetic model mapping.
func TestReview21TranslatingTransport(t *testing.T) {
	h := newSentHarness(t, reviewRouter)
	useAnsweredBook(t, newSentClock())
	got := make(chan string, 1)
	vendor := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- r.Host + " " + ojson.GetBytes(b, "model").String()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, reviewChat)
	}))
	defer vendor.Close()
	network := vendor.Client().Transport.(*http.Transport).Clone()
	network.TLSClientConfig.ServerName = "example.com"
	network.DialContext = func(c context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(c, "tcp", vendor.Listener.Addr().String())
	}
	defer network.CloseIdleConnections()
	reviewSet(h, reviewRT(func(req *http.Request) (*http.Response, error) {
		r := req.Clone(req.Context())
		raw, _ := io.ReadAll(req.Body)
		req.Body.Close()
		out := bytes.Replace(raw, []byte(`"lab/native"`), []byte(`"lab/proxy-target"`), 1)
		r.Body = io.NopCloser(bytes.NewReader(out))
		r.ContentLength = int64(len(out))
		if r.Header.Get("Content-Length") != "" {
			r.Header.Set("Content-Length", strconv.Itoa(len(out)))
		}
		return network.RoundTrip(r)
	}))
	ans := h.post("/v1/chat/completions", chatBody("review-public", false, ""), nil)
	if ans.status != 200 {
		t.Fatalf("fixture HTTP %d", ans.status)
	}
	wire := <-got
	if wire != "openrouter.ai lab/proxy-target" {
		t.Fatal("fixture endpoint", wire)
	}
	entry := ans.get(logging.ProvimuxEntryHeader)
	t.Logf("TLS vendor received %q, client received %q", wire, entry)
	if entry != "" && entry != "openrouter/lab/proxy-target" {
		t.Error("entry names a model preceding the request translator")
	}
}
