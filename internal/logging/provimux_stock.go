package logging

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"sync"
)

// Provimux: the stock output route (.github/provimux/design/answered-by.md, the lead's amendment of 2026-11-27 (2),
// rule B.1). The record of what answers said keeps exactly what the gateway's own output route wrote to the client,
// read at the moment it writes. The gateway's own HTTP server (internal/api.NewServer, built with none of the SDK's
// options that put another program's code on the response path) marks every request it serves through its base
// context, with a key no other package can make, and its handler holds, directly over the writer net/http hands it, a
// writer that reads the X-Provimux-* headers at the moment the response's headers are committed. A response written
// through any other server (an embedding that serves the gateway's handler itself, with a writer of its own) is not
// marked, and records nothing.

type provimuxStockKey struct{}

// ProvimuxStockServer makes srv the gateway's stock output route: its requests carry the mark, and its handler reads
// what is committed. Only internal/api.NewServer calls it, for a server built with none of those options (stock).
func ProvimuxStockServer(srv *http.Server, stock bool) {
	if srv == nil || !stock {
		return
	}
	base := srv.BaseContext
	srv.BaseContext = func(listener net.Listener) context.Context {
		ctx := context.Background()
		if base != nil {
			ctx = base(listener)
		}
		return context.WithValue(ctx, provimuxStockKey{}, true)
	}
	next := srv.Handler
	if next == nil {
		next = http.DefaultServeMux
	}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ProvimuxStockRequest(r.Context()) || !ProvimuxAnsweredBy() {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&provimuxStockWriter{ResponseWriter: w}, r)
	})
}

// ProvimuxStockRequest reports whether ctx is a request's the gateway's stock output route serves.
func ProvimuxStockRequest(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	stock, _ := ctx.Value(provimuxStockKey{}).(bool)
	return stock
}

// provimuxStockWriter sits directly over the writer net/http hands the stock route, and reads the statement's headers
// when the response's headers are committed. It changes nothing it passes on.
type provimuxStockWriter struct {
	http.ResponseWriter

	mu        sync.Mutex
	committed bool
	account   string
	entries   []string
}

// see reads, once, the headers being committed.
func (w *provimuxStockWriter) see() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.committed {
		return
	}
	w.committed = true
	header := w.ResponseWriter.Header()
	w.account = header.Get(ProvimuxAccountHeader)
	w.entries = append([]string(nil), header.Values(ProvimuxEntryHeader)...)
}

// written returns what the committed headers said: the marker and the entry values. ok is false before a commit.
func (w *provimuxStockWriter) written() (account string, entries []string, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.account, w.entries, w.committed
}

func (w *provimuxStockWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *provimuxStockWriter) WriteHeader(code int) {
	if code >= http.StatusOK || code == http.StatusSwitchingProtocols {
		w.see()
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *provimuxStockWriter) Write(data []byte) (int, error) {
	w.see()
	return w.ResponseWriter.Write(data)
}

func (w *provimuxStockWriter) WriteString(data string) (int, error) {
	w.see()
	return io.WriteString(w.ResponseWriter, data)
}

func (w *provimuxStockWriter) Flush() { _ = w.FlushError() }

func (w *provimuxStockWriter) FlushError() error {
	w.see()
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *provimuxStockWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

func (w *provimuxStockWriter) CloseNotify() <-chan bool {
	if notifier, ok := w.ResponseWriter.(http.CloseNotifier); ok { //nolint:staticcheck // gin's writer asks for it
		return notifier.CloseNotify()
	}
	return nil
}

func (w *provimuxStockWriter) Push(target string, opts *http.PushOptions) error {
	if pusher, ok := w.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, opts)
	}
	return http.ErrNotSupported
}

// provimuxStockOf finds, below w, the stock route's writer: w and the writers it wraps (Unwrap).
func provimuxStockOf(w http.ResponseWriter) *provimuxStockWriter {
	for depth := 0; w != nil && depth < 16; depth++ {
		if stock, ok := w.(*provimuxStockWriter); ok {
			return stock
		}
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return nil
		}
		w = unwrapper.Unwrap()
	}
	return nil
}
