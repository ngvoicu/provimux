package logging

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Provimux: the stock output route (the lead's amendment of 2026-11-27 (2), rule B.1). A server ProvimuxStockServer made
// marks its requests and reads, at the commit, the statement's headers as they go out; the same handler served by any
// other server is not marked and reads nothing. The writer changes nothing it passes on.

func stockServe(t *testing.T, stock bool, handler http.HandlerFunc) (*httptest.Server, chan *provimuxStockWriter) {
	t.Helper()
	was := SetProvimuxAnsweredBy(true)
	t.Cleanup(func() { SetProvimuxAnsweredBy(was) })
	writers := make(chan *provimuxStockWriter, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler(w, r)
		writers <- provimuxStockOf(w)
	})}
	ProvimuxStockServer(srv, stock)
	server := httptest.NewUnstartedServer(nil)
	server.Config = srv
	server.Start()
	t.Cleanup(server.Close)
	return server, writers
}

func TestProvimuxStockRouteReadsWhatIsCommitted(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stock   bool
		write   func(w http.ResponseWriter)
		account string
		entries []string
		seen    bool
	}{
		{"the account and an entry", true, func(w http.ResponseWriter) {
			w.Header().Set(ProvimuxAccountHeader, "a.aaaaaaaaaaaaaaaaaaaa")
			w.Header().Set(ProvimuxEntryHeader, "lab/entry")
			w.WriteHeader(http.StatusOK)
			w.Header().Del(ProvimuxEntryHeader) // after the commit: not what went out's concern
		}, "a.aaaaaaaaaaaaaaaaaaaa", []string{"lab/entry"}, true},
		{"the account and no entry", true, func(w http.ResponseWriter) {
			w.Header().Set(ProvimuxAccountHeader, "a.aaaaaaaaaaaaaaaaaaaa")
			_, _ = io.WriteString(w, "{}")
		}, "a.aaaaaaaaaaaaaaaaaaaa", nil, true},
		{"no statement", true, func(w http.ResponseWriter) { _, _ = w.Write([]byte("{}")) }, "", nil, true},
		{"a flush commits", true, func(w http.ResponseWriter) {
			w.Header().Set(ProvimuxAccountHeader, "a.aaaaaaaaaaaaaaaaaaaa")
			w.(http.Flusher).Flush()
		}, "a.aaaaaaaaaaaaaaaaaaaa", nil, true},
		{"another server", false, func(w http.ResponseWriter) {
			w.Header().Set(ProvimuxAccountHeader, "a.aaaaaaaaaaaaaaaaaaaa")
			w.WriteHeader(http.StatusOK)
		}, "", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, writers := stockServe(t, tc.stock, func(w http.ResponseWriter, r *http.Request) {
				if ProvimuxStockRequest(r.Context()) != tc.stock {
					t.Errorf("marked %v, want %v", !tc.stock, tc.stock)
				}
				tc.write(w)
			})
			resp, err := server.Client().Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			stock := <-writers
			if (stock != nil) != tc.seen {
				t.Fatalf("the stock writer is there: %v, want %v", stock != nil, tc.seen)
			}
			if stock == nil {
				return
			}
			account, entries, committed := stock.written()
			if !committed || account != tc.account || strings.Join(entries, ",") != strings.Join(tc.entries, ",") {
				t.Fatalf("read %v %q %q, want %q %q", committed, account, entries, tc.account, tc.entries)
			}
			if got := resp.Header.Get(ProvimuxAccountHeader); got != tc.account {
				t.Fatalf("the client got %q, the writer read %q", got, account)
			}
		})
	}
}

// The writer hands on a hijack (a WebSocket upgrade) and every byte as it is.
func TestProvimuxStockRouteChangesNothing(t *testing.T) {
	server, writers := stockServe(t, true, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hijack" {
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = rw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
			_ = rw.Flush()
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "provimux-test-body")
	})
	for _, path := range []string{"/", "/hijack"} {
		conn, err := net.Dial("tcp", server.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(conn, "GET "+path+" HTTP/1.1\r\nHost: provimux-test\r\nConnection: close\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = conn.Close()
		<-writers
		want := map[string]string{"/": "provimux-test-body", "/hijack": "ok"}[path]
		if string(body) != want {
			t.Fatalf("%s: %q, want %q", path, body, want)
		}
	}
	if ProvimuxStockRequest(context.Background()) {
		t.Fatal("a context the stock route did not make is marked")
	}
}
