package modellists

// Published lists: providers.dev's listings (model-lists.md 4.2, 5.2-5.4; step P3).
//
// One request, the same for every user: GET /api/v1/listings?view=full&limit=40&provider=<the fixed set
// of Provimux's map>, with If-None-Match and a User-Agent and nothing else. Since API 1.5.1 a page in
// full holds at most 40 listings and providers.dev has hundreds, so the one request of section 4.2
// (every listing) cannot be one page; the documented provider filter keeps it one request, and the
// filter is a constant of Provimux, so it says nothing about which logins a user holds.
//
// A fetched document replaces the held one only when it is a 200, well-formed, complete (every listing
// the filter matched is on the page), at most 8 MB, and not older than the held one. A listing that is
// missing, empty or malformed keeps the held listing of its provider only (section 5.3).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
)

const (
	// PublishedFileName is the disk copy of the last accepted document, beside the live files.
	PublishedFileName = "published.json"
	// PublishedSchema is the version of the disk copy.
	PublishedSchema = 1
	// FullListingLimit is the largest page providers.dev serves with view=full (API 1.5.1).
	FullListingLimit = 40
	// maxPublishedBytes bounds an answer, as the live fetch bounds one (section 5.3).
	maxPublishedBytes = 8 << 20
)

// PublishedURL is providers.dev's listings resource. Tests point the client elsewhere; nothing in the
// config changes it.
const PublishedURL = "https://providers.dev/api/v1/listings"

// listingProviders maps a login type to its providers.dev listing (model-lists.md 4.1). The map lives in
// Provimux: the API knows providers, not Provimux. Which Kimi host is "global" is inferred (4.1).
var listingProviders = map[string]string{
	"claude":      "claude-subscription",
	"codex":       "openai-codex",
	"copilot":     "github-copilot",
	"antigravity": "antigravity",
	"devin":       "devin",
	"meta":        "meta-subscription",
	"kimi":        "kimi-code-plan-global",
	"xai":         "xai",
	"gemini":      "google",
	"aistudio":    "google",
	"vertex":      "google-vertex",
}

// ListingProvider returns the providers.dev listing of a login type, or "".
func ListingProvider(loginType string) string {
	return listingProviders[strings.ToLower(strings.TrimSpace(loginType))]
}

// LoginTypesOf returns the login types whose listing is provider, sorted.
func LoginTypesOf(provider string) []string {
	var out []string
	for loginType, listing := range listingProviders {
		if listing == provider {
			out = append(out, loginType)
		}
	}
	sort.Strings(out)
	return out
}

// ListingProviders returns every listing of the map, sorted and unique: the request's filter.
func ListingProviders() []string {
	seen := map[string]struct{}{}
	var out []string
	for _, provider := range listingProviders {
		if _, dup := seen[provider]; !dup {
			seen[provider] = struct{}{}
			out = append(out, provider)
		}
	}
	sort.Strings(out)
	return out
}

// PublishedRequestURL is the one request Provimux sends to providers.dev, built on base.
func PublishedRequestURL(base string) string {
	query := url.Values{}
	query.Set("view", "full")
	query.Set("limit", fmt.Sprint(FullListingLimit))
	query.Set("provider", strings.Join(ListingProviders(), ","))
	return base + "?" + query.Encode()
}

// Reasoning is a listing row's reasoning: nil fields are not stated.
type Reasoning struct {
	Supported   *bool    `json:"supported"`
	Levels      []string `json:"levels"`
	BudgetMin   *int     `json:"budget_min"`
	BudgetMax   *int     `json:"budget_max"`
	CanDisable  *bool    `json:"can_disable"`
	Dynamic     *bool    `json:"dynamic"`
	DisableWith *string  `json:"disable_with"`
}

// ListingModel is one row of a listing, the fields Provimux reads. Nil or empty means not stated.
type ListingModel struct {
	ID               string     `json:"id"`
	Name             *string    `json:"name"`
	ContextWindow    *int       `json:"context_window"`
	MaxInput         *int       `json:"max_input"`
	MaxOutput        *int       `json:"max_output"`
	InputModalities  []string   `json:"input_modalities"`
	OutputModalities []string   `json:"output_modalities"`
	Reasoning        *Reasoning `json:"reasoning"`
	WebSearch        *bool      `json:"web_search"`
	Aliases          []string   `json:"aliases"`
	Variants         []string   `json:"variants"`
}

// Listing is one provider's model list.
type Listing struct {
	Provider string         `json:"provider"`
	Basis    string         `json:"basis"`
	ListedAt *string        `json:"listed_at"`
	Models   []ListingModel `json:"models"`
}

// Document is an accepted providers.dev answer, as held in memory and on disk.
type Document struct {
	Schema         int                `json:"schema"`
	ETag           string             `json:"etag"`
	DatasetVersion string             `json:"dataset_version"`
	GeneratedAt    time.Time          `json:"generated_at"`
	FetchedAt      time.Time          `json:"fetched_at"`
	Listings       map[string]Listing `json:"listings"` // by provider
}

// Failure categories of a published fetch: the live lists' closed set.
const (
	FailureHTTP     = "http_status"
	FailureResponse = "invalid_response"
	FailureNetwork  = "network"
)

// FetchError is a failed fetch: a category and, for http_status, the status. It never carries the
// answer's text.
type FetchError struct {
	Category string
	Status   int
	err      error
}

func (e *FetchError) Error() string {
	if e.Category == FailureHTTP {
		return fmt.Sprintf("providers.dev: HTTP %d", e.Status)
	}
	if e.err != nil {
		return "providers.dev: " + e.Category + ": " + e.err.Error()
	}
	return "providers.dev: " + e.Category
}

func (e *FetchError) Unwrap() error { return e.err }

// ErrOlder means the fetched document was generated before the held one.
var ErrOlder = errors.New("document older than the held one")

func failure(category string, err error) *FetchError {
	return &FetchError{Category: category, err: err}
}

// FetchResult is a fetch that got an answer: 304, or a well-formed and complete document. Bad names
// the listings of the filter that were missing, empty or malformed: each keeps its held listing.
type FetchResult struct {
	NotModified bool
	Document    *Document
	Bad         []string
}

// Fetch sends the one request. etag is the held document's ("" without one); userAgent is
// "provimux/<version>". It sends no other header and no cookie.
func Fetch(ctx context.Context, client *http.Client, base, etag, userAgent string, now time.Time) (FetchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, PublishedRequestURL(base), nil)
	if err != nil {
		return FetchResult{}, failure(FailureNetwork, err)
	}
	req.Header.Set("User-Agent", userAgent)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return FetchResult{}, failure(FailureNetwork, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotModified && etag != "" {
		return FetchResult{NotModified: true}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return FetchResult{}, &FetchError{Category: FailureHTTP, Status: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPublishedBytes+1))
	if err != nil {
		// A body cut off in transfer: the answer never arrived whole.
		return FetchResult{}, failure(FailureNetwork, err)
	}
	if len(body) > maxPublishedBytes {
		return FetchResult{}, failure(FailureResponse, errors.New("answer larger than 8 MB"))
	}
	doc, bad, err := parseListingsPage(body)
	if err != nil {
		return FetchResult{}, failure(FailureResponse, err)
	}
	doc.ETag = strings.TrimSpace(resp.Header.Get("ETag"))
	doc.FetchedAt = now.UTC()
	return FetchResult{Document: doc, Bad: bad}, nil
}

// parseListingsPage reads one page of GET /api/v1/listings?view=full. The page must be whole: every
// listing the filter matched is on it. Listings are checked one by one.
func parseListingsPage(body []byte) (*Document, []string, error) {
	var page struct {
		Meta *struct {
			DatasetVersion string `json:"dataset_version"`
			GeneratedAt    string `json:"generated_at"`
		} `json:"meta"`
		Total      *int              `json:"total"`
		NextOffset *int              `json:"next_offset"`
		Data       []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, nil, fmt.Errorf("malformed answer: %w", err)
	}
	if page.Meta == nil || page.Data == nil || page.Total == nil {
		return nil, nil, errors.New("malformed answer: meta, total or data missing")
	}
	generated, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(page.Meta.GeneratedAt))
	if err != nil {
		return nil, nil, errors.New("malformed answer: generated_at")
	}
	if page.NextOffset != nil || *page.Total != len(page.Data) {
		return nil, nil, fmt.Errorf("incomplete answer: %d of %d listings", len(page.Data), *page.Total)
	}
	wanted := map[string]bool{}
	for _, provider := range ListingProviders() {
		wanted[provider] = false
	}
	doc := &Document{Schema: PublishedSchema, DatasetVersion: strings.TrimSpace(page.Meta.DatasetVersion),
		GeneratedAt: generated.UTC(), Listings: map[string]Listing{}}
	for _, raw := range page.Data {
		var listing Listing
		if errListing := json.Unmarshal(raw, &listing); errListing != nil {
			continue // malformed: whichever provider it was stays missing, so its held listing stays
		}
		if seen, ok := wanted[listing.Provider]; !ok || seen {
			continue // not asked for, or twice
		}
		wanted[listing.Provider] = true
		if listing.Valid() {
			doc.Listings[listing.Provider] = listing
		}
	}
	var bad []string
	for _, provider := range ListingProviders() {
		if _, ok := doc.Listings[provider]; !ok {
			bad = append(bad, provider)
		}
	}
	return doc, bad, nil
}

// Valid reports whether a listing can replace a held one: it names at least one model and every row
// has an id.
func (l Listing) Valid() bool {
	if strings.TrimSpace(l.Provider) == "" || len(l.Models) == 0 {
		return false
	}
	for _, model := range l.Models {
		if strings.TrimSpace(model.ID) == "" {
			return false
		}
	}
	return true
}

// Accept merges a fetched document into the held one (nil without one). A document generated before the
// held one is refused (ErrOlder). A listing of bad keeps the held listing of that provider, if any.
// It returns the new document and the providers whose listing changed.
func Accept(held *Document, fetched *Document) (*Document, []string, error) {
	if fetched == nil {
		return held, nil, nil
	}
	if held != nil && fetched.GeneratedAt.Before(held.GeneratedAt) {
		return held, nil, ErrOlder
	}
	merged := &Document{Schema: PublishedSchema, ETag: fetched.ETag, DatasetVersion: fetched.DatasetVersion,
		GeneratedAt: fetched.GeneratedAt, FetchedAt: fetched.FetchedAt, Listings: map[string]Listing{}}
	var changed []string
	for _, provider := range ListingProviders() {
		next, ok := fetched.Listings[provider]
		var previous Listing
		var had bool
		if held != nil {
			previous, had = held.Listings[provider]
		}
		if !ok {
			if had {
				merged.Listings[provider] = previous
			}
			continue
		}
		merged.Listings[provider] = next
		if !had || !sameListing(previous, next) {
			changed = append(changed, provider)
		}
	}
	return merged, changed, nil
}

func sameListing(a, b Listing) bool {
	left, errLeft := json.Marshal(a)
	right, errRight := json.Marshal(b)
	return errLeft == nil && errRight == nil && string(left) == string(right)
}

// ReadPublished returns the disk copy of the last accepted document. ErrNotCached without one; any
// other error means the file was damaged or foreign and was ignored (warned once).
func (s *Store) ReadPublished() (*Document, error) {
	if s == nil || s.dir == "" {
		return nil, ErrNotCached
	}
	path := filepath.Join(s.dir, PublishedFileName)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotCached
		}
		s.warnOnce("read:"+PublishedFileName, "model lists cache: cannot read %s: %v", PublishedFileName, err)
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxPublishedBytes+1))
	if err == nil && len(data) > maxPublishedBytes {
		err = fmt.Errorf("larger than %d bytes", maxPublishedBytes)
	}
	var doc Document
	if err == nil {
		err = json.Unmarshal(data, &doc)
	}
	if err == nil {
		err = doc.check()
	}
	if err != nil {
		s.warnOnce("bad:"+PublishedFileName, "model lists cache: ignoring %s (%v); the next accepted document replaces it", PublishedFileName, err)
		return nil, err
	}
	return &doc, nil
}

func (d Document) check() error {
	switch {
	case d.Schema != PublishedSchema:
		return fmt.Errorf("schema %d", d.Schema)
	case d.GeneratedAt.IsZero() || d.FetchedAt.IsZero():
		return errors.New("no dates")
	}
	for provider, listing := range d.Listings {
		if listing.Provider != provider || !listing.Valid() {
			return fmt.Errorf("listing %q is not valid", provider)
		}
	}
	return nil
}

// WritePublished stores the accepted document: mode 0600, a temporary file renamed over the old one.
func (s *Store) WritePublished(doc *Document) error {
	if s == nil || s.dir == "" || doc == nil {
		return nil
	}
	data, err := json.Marshal(doc)
	if err == nil {
		err = WriteFileAtomic(s.dir, PublishedFileName, data)
	}
	if err != nil {
		s.warnOnce("write", "model lists cache: cannot write to %s (%v); lists stay in memory only", s.dir, err)
		return err
	}
	s.clearWarning("bad:" + PublishedFileName)
	s.clearWarning("write")
	return nil
}
