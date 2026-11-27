// Package pdentries is a stand-in of providers.dev's GET /api/v1/entries on loopback, for Provimux's tests
// of the figures (prices-and-cache.md 6.2). It follows the contract of providers.dev's API (OpenAPI
// 1.6.3, the site's src/core/openapi.ts and query.ts): the parameters provider (comma-separated), limit (1 to
// 200, default 50) and offset; any other parameter is a 400 invalid_parameters; the envelope { meta, total,
// limit, offset, next_offset, data }; entries ordered by provider id, then model id; an opaque ETag per
// response, and 304 with no body for a matching If-None-Match. Only tests import it; no test reaches the
// real site.
package pdentries

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/MoeForge/moego/json"
)

// Path is where the stand-in serves the entries.
const Path = "/api/v1/entries"

// Server is the stand-in. Its fields under mu may be changed while it serves.
type Server struct {
	*httptest.Server

	mu        sync.Mutex
	entries   []json.RawMessage
	version   string
	generated string
	requests  []*http.Request
	// page, when set, may replace a page's status and body: it gets the offset and the body the stand-in
	// would send, and returns what to send instead (status 0: unchanged).
	page func(offset int, body []byte) (int, []byte)
	// hang, when set, makes every request wait until it is closed or the client gives up.
	hang chan struct{}
}

// New starts a stand-in serving entries as the dataset version of generated (RFC 3339).
func New(entries []json.RawMessage, version, generated string) *Server {
	s := &Server{entries: entries, version: version, generated: generated}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// EntriesURL is the entries resource.
func (s *Server) EntriesURL() string { return s.URL + Path }

// SetDataset replaces the data and the dataset, as a new publication does.
func (s *Server) SetDataset(entries []json.RawMessage, version, generated string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries, s.version, s.generated = entries, version, generated
}

// SetPage sets or clears the page hook.
func (s *Server) SetPage(page func(offset int, body []byte) (int, []byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.page = page
}

// SetHang sets or clears the hang.
func (s *Server) SetHang(hang chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hang = hang
}

// Requests returns the requests received so far.
func (s *Server) Requests() []*http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*http.Request(nil), s.requests...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests = append(s.requests, r.Clone(context.Background()))
	entries, version, generated, page, hang := s.entries, s.version, s.generated, s.page, s.hang
	s.mu.Unlock()
	if hang != nil {
		select {
		case <-hang:
		case <-r.Context().Done():
		}
		return
	}
	if r.URL.Path != Path || r.Method != http.MethodGet {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	query := r.URL.Query()
	for name := range query {
		if name != "provider" && name != "limit" && name != "offset" {
			writeError(w, http.StatusBadRequest, "invalid_parameters")
			return
		}
	}
	limit, offset := 50, 0
	if raw := query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			writeError(w, http.StatusBadRequest, "invalid_parameters")
			return
		}
		limit = n
	}
	if raw := query.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid_parameters")
			return
		}
		offset = n
	}
	wanted := map[string]bool{}
	for _, provider := range strings.Split(query.Get("provider"), ",") {
		if provider = strings.TrimSpace(provider); provider != "" {
			wanted[provider] = true
		}
	}
	type row struct {
		provider, model string
		raw             json.RawMessage
	}
	var rows []row
	for _, raw := range entries {
		var head struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		}
		_ = json.Unmarshal(raw, &head)
		if len(wanted) == 0 || wanted[head.Provider] {
			rows = append(rows, row{head.Provider, head.Model, raw})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].provider != rows[j].provider {
			return rows[i].provider < rows[j].provider
		}
		return rows[i].model < rows[j].model
	})
	data := []json.RawMessage{}
	for i := offset; i < len(rows) && i < offset+limit; i++ {
		data = append(data, rows[i].raw)
	}
	var next any
	if offset+limit < len(rows) {
		next = offset + limit
	}
	body, err := json.Marshal(map[string]any{
		"meta":  map[string]any{"dataset_version": version, "generated_at": generated, "upstream_fetched_at": generated},
		"total": len(rows), "limit": limit, "offset": offset, "next_offset": next, "data": data,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	status := http.StatusOK
	if page != nil {
		if replaced, replacedBody := page(offset, body); replaced != 0 {
			status, body = replaced, replacedBody
		}
	}
	sum := sha256.Sum256(append([]byte(r.URL.RawQuery+"\x00"), body...))
	etag := `W/"` + hex.EncodeToString(sum[:]) + `"`
	if status == http.StatusOK {
		w.Header().Set("ETag", etag)
		w.Header().Set("X-Dataset-Version", version)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"error":{"code":%q,"message":%q}}`, code, code)
}

// Copy returns summary with its model id (and ref) set to model: a further entry of the same shape.
func Copy(summary json.RawMessage, model string) json.RawMessage {
	var entry map[string]any
	if err := json.Unmarshal(summary, &entry); err != nil {
		return summary
	}
	provider, _ := entry["provider"].(string)
	entry["model"], entry["ref"] = model, provider+"/"+model
	out, err := json.Marshal(entry)
	if err != nil {
		return summary
	}
	return out
}

// Edit returns summary with edit applied to its decoded form.
func Edit(summary json.RawMessage, edit func(entry map[string]any)) json.RawMessage {
	var entry map[string]any
	if err := json.Unmarshal(summary, &entry); err != nil {
		return summary
	}
	edit(entry)
	out, err := json.Marshal(entry)
	if err != nil {
		return summary
	}
	return out
}
