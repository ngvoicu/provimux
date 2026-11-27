package cliproxy

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type followVendor struct {
	mu   sync.Mutex
	seen []string
}

func (s *followVendor) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		req := r.Clone(r.Context())
		req.URL.Host = r.Host
		req.URL.Scheme = "https"
		id := standInModel(t, req, b)
		s.mu.Lock()
		s.seen = append(s.seen, r.Host+" "+r.URL.Path+" "+id)
		s.mu.Unlock()
		if r.URL.Path == "/api/oauth/profile" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"account":{"uuid":"8f5ac379-aa55-4ade-9eef-5cab1b70e841"},"organization":{"uuid":"7f5ac379-aa55-4ade-9eef-5cab1b70e841"}}`)
			return
		}
		resp := standInAnswer(req, b, id)
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		resp.Body.Close()
	})
}
func (s *followVendor) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ret := s.seen
	s.seen = nil
	return ret
}
func followRealNetwork(t *testing.T, server *httptest.Server, serverName string) *http.Transport {
	tr := server.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig.ServerName = serverName
	tr.DialContext = func(c context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(c, "tcp", server.Listener.Addr().String())
	}
	t.Cleanup(tr.CloseIdleConnections)
	return tr
}

// The ordinary paths: each ordinary answer of a built-in executor, over a real TLS receiver, names the entry of what
// the receiver got.
func TestFollowNativeOrdinary(t *testing.T) {
	h := newSentHarness(t, sentMatrixConfig)
	useAnsweredBook(t, newSentClock())
	sentLogins(t, h)
	sentCopilot(t, h, "provimux-test-copilot")
	s := &followVendor{}
	server := httptest.NewTLSServer(s.handler(t))
	defer server.Close()
	reviewSet(h, followRealNetwork(t, server, "example.com"))
	indexes := []int{0, 5, 6, 8, 12, 13, 15, 16, 17, 20, 21, 22}
	for _, i := range indexes {
		tc := sentMatrix()[i]
		t.Run(tc.name, func(t *testing.T) {
			s.take()
			a := h.post(tc.path, tc.body, nil)
			receipts := s.take()
			entry := a.get(logging.ProvimuxEntryHeader)
			t.Logf("HTTP %d, receipts=%v entry=%q", a.status, receipts, entry)
			if a.status != 200 || len(receipts) == 0 {
				t.Fatalf("fixture HTTP %d %s", a.status, a.body)
			}
			receipt := strings.SplitN(receipts[len(receipts)-1], " ", 3)
			want := strings.ReplaceAll(tc.entry, "{sent}", receipt[2])
			if want != entry {
				t.Errorf("ordinary answer want %q", want)
			}
		})
	}
}
