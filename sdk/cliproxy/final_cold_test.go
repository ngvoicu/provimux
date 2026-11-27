package cliproxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"

	ca "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var followRootOnce sync.Once
var followCert tls.Certificate

func followClaudeCert() {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	display := "api.anthropic.com"
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: display}, DNSNames: []string{display}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	// Ported: where the system has roots of its own (a container with a CA bundle, a Mac), Go uses the fallback roots
	// only with x509usefallbackroots=1; the setting is read when they are set, and the environment is restored after.
	was, had := os.LookupEnv("GODEBUG")
	_ = os.Setenv("GODEBUG", strings.TrimPrefix(was+",x509usefallbackroots=1", ","))
	x509.SetFallbackRoots(pool)
	if had {
		_ = os.Setenv("GODEBUG", was)
	} else {
		_ = os.Unsetenv("GODEBUG")
	}
	followCert = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}
}

// The cold Claude login: the first answer fetches the account's profile and
// then names the subscription entry of the model received; the warm one names it too.
func TestFollowColdClaude(t *testing.T) {
	followRootOnce.Do(followClaudeCert)
	h := newSentHarness(t, "")
	useAnsweredBook(t, newSentClock())
	s := &followVendor{}
	server := httptest.NewUnstartedServer(s.handler(t))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{followCert}}
	server.StartTLS()
	defer server.Close()
	tunnel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.Host != "api.anthropic.com:443" {
			http.Error(w, "fixture rejects destination", 403)
			return
		}
		dst, e := net.Dial("tcp", server.Listener.Addr().String())
		if e != nil {
			http.Error(w, "fixture", 500)
			return
		}
		src, b, e := w.(http.Hijacker).Hijack()
		if e != nil {
			dst.Close()
			return
		}
		fmt.Fprint(b, "HTTP/1.1 200 Connection Established\r\n\r\n")
		b.Flush()
		go func() { defer dst.Close(); io.Copy(dst, src) }()
		defer src.Close()
		io.Copy(src, dst)
	}))
	defer tunnel.Close()
	h.service.coreManager.SetRoundTripperProvider(nil)
	model := "follow-claude-oauth"
	h.register(&ca.Auth{ID: "follow-claude.json", FileName: "follow-claude.json", Provider: "claude", Status: ca.StatusActive, ProxyURL: tunnel.URL, Attributes: map[string]string{"auth_kind": "oauth", "path": filepath.Join(t.TempDir(), "follow-claude.json")}, Metadata: map[string]any{"type": "claude", "access_token": "sk-ant-oat01-fixture-not-a-real-token", "expired": sentFar()}}, model)
	for i := 0; i < 2; i++ {
		a := h.post("/v1/chat/completions", chatBody(model, false, ""), nil)
		got := s.take()
		entry := a.get(logging.ProvimuxEntryHeader)
		t.Logf("call=%d HTTP=%d receipts=%v entry=%q", i+1, a.status, got, entry)
		if a.status != 200 {
			t.Fatalf("fixture %s", a.body)
		}
		if i == 0 && (!strings.Contains(fmt.Sprint(got), "/api/oauth/profile") || len(got) < 2) {
			t.Fatal("cold fixture did not fetch a profile")
		}
		if i == 0 && entry != "claude-subscription/"+model {
			t.Error("the cold answer did not name the model received")
		}
		if i == 1 && entry != "claude-subscription/"+model {
			t.Error("warm login failed to name its entry")
		}
	}
}
