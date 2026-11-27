package modellists

// Figures: providers.dev's prices and cache lifetimes of the entries (prices-and-cache.md 4 and 6.2;
// step F2). The contract of the kept record is .github/provimux/design/prices-and-cache/openapi.yaml,
// components/schemas/Entry.
//
// One request, the same for every user: GET /api/v1/entries?provider=<the fixed set>&limit=200&offset=<0,
// 200, ...>, page after page until next_offset is null (at most 10), each page with If-None-Match when its
// ETag is held, a User-Agent and nothing else; a redirect is followed only to the same host over https. A
// fetched document replaces the held one only when every page is a 200 or a 304, well-formed, at most 8 MB,
// every page names one dataset_version and one total, together they hold total entries, at least one entry
// can be read, generated_at is not before the held document's and not more than 24 hours ahead of the
// gateway's clock. An entry Provimux cannot read is left out alone, and so is a value only a broken summary
// carries. Nothing here decides which entry an answer is (6.1); this only holds figures.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
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
	// FiguresFileName is the disk copy of the last accepted figures, beside published.json.
	FiguresFileName = "figures.json"
	// FiguresSchema is the version of the disk copy. A change of the kept record or of the rule of
	// prices-and-cache.md 4.2 raises it, so an older file is ignored and replaced by the next fetch.
	FiguresSchema = 1
	// FiguresPageLimit is the page size Provimux asks for: providers.dev's largest (API 1.6).
	FiguresPageLimit = 200
	// FiguresMaxPages bounds the pages of one fetch.
	FiguresMaxPages = 10
	// FiguresContract is the contract number of the kept record (prices-and-cache.md 5).
	FiguresContract = 1
	// FiguresSource is where the figures come from.
	FiguresSource = "https://providers.dev"
	// maxFiguresPageBytes bounds one page and the disk copy, as the listings are bounded.
	maxFiguresPageBytes = 8 << 20
	// FiguresMaxAhead is how far a document's generated_at may be ahead of the gateway's clock; a later one is
	// refused, from the network and from the disk copy (decided by the lead on 2026-11-27).
	FiguresMaxAhead = 24 * time.Hour
	// maxFiguresTokens is the largest token count kept: the largest integer a JSON number carries exactly. A
	// larger window, limit or tier is left out, never clamped.
	maxFiguresTokens = 1<<53 - 1
	// maxFiguresRedirects bounds the redirects one page follows.
	maxFiguresRedirects = 10
)

// Why a fetched or stored document is refused, beside the codes of FetchError (decided by the lead on
// 2026-11-27). Each is wrapped in a FetchError of category invalid_response.
var (
	// ErrNoEntries: no entry of the document can be read (every one unreadable, or total 0).
	ErrNoEntries = errors.New("no entry Provimux can read")
	// ErrAhead: generated_at is more than FiguresMaxAhead ahead of the gateway's clock.
	ErrAhead = errors.New("generated_at more than 24 hours ahead of the gateway's clock")
)

// EntriesURL is providers.dev's entries resource. Tests point the client elsewhere; nothing in the config
// changes it.
const EntriesURL = "https://providers.dev/api/v1/entries"

// entryPageBase is the site's page of an entry; a record's url is built on it whatever base was asked.
const entryPageBase = "https://providers.dev/entries/"

// figureProviders is the fixed set of prices-and-cache.md 6.2: the same for every user, whatever accounts
// he holds, so the request tells the server nothing about him.
var figureProviders = []string{
	"anthropic", "antigravity", "claude-subscription", "devin", "github-copilot", "google", "google-vertex",
	"meta-subscription", "openai", "openai-codex", "openrouter", "xai",
}

// FigureProviders returns the fixed set the figures request asks for, sorted.
func FigureProviders() []string {
	out := append([]string(nil), figureProviders...)
	sort.Strings(out)
	return out
}

func isFigureProvider(provider string) bool {
	for _, p := range figureProviders {
		if p == provider {
			return true
		}
	}
	return false
}

// FiguresRequestURL is one page of the request Provimux sends, built on base.
func FiguresRequestURL(base string, offset int) string {
	query := url.Values{}
	query.Set("provider", strings.Join(FigureProviders(), ","))
	query.Set("limit", fmt.Sprint(FiguresPageLimit))
	query.Set("offset", fmt.Sprint(offset))
	return base + "?" + query.Encode()
}

// EntryPageURL is the entry's page on providers.dev: each segment of the reference escaped on its own.
func EntryPageURL(provider, model string) string {
	segments := strings.Split(EntryRef(provider, model), "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return entryPageBase + strings.Join(segments, "/") + "/"
}

// ---- the kept record: openapi.yaml, components/schemas/Entry and below

// Figures is what Provimux keeps of one entry.
type Figures struct {
	Provider    string       `json:"provider"`
	Model       string       `json:"model"`
	URL         string       `json:"url"`
	Access      string       `json:"access"`
	Price       EntryPrice   `json:"price"`
	Cache       EntryCache   `json:"cache"`
	InputWindow FiguresLimit `json:"input_window"`
	MaxOutput   FiguresLimit `json:"max_output"`
}

// EntryPrice is an entry's price: US dollars per million tokens.
type EntryPrice struct {
	Billing       string           `json:"billing"`
	Reference     *string          `json:"reference"`
	Input         *float64         `json:"input"`
	Output        *float64         `json:"output"`
	CacheRead     *float64         `json:"cache_read"`
	CacheWrite    []CacheWriteRate `json:"cache_write"`
	Tiers         []PriceTier      `json:"tiers"`
	OtherRates    bool             `json:"other_rates"`
	Source        *string          `json:"source"`
	Date          *string          `json:"date"`
	SourcesDiffer bool             `json:"sources_differ"`
}

// CacheWriteRate is the price of a cache write for one lifetime; LifetimeS nil where the source does not
// key the price by lifetime.
type CacheWriteRate struct {
	LifetimeS *int    `json:"lifetime_s"`
	Price     float64 `json:"price"`
}

// PriceTier is a price above a prompt size.
type PriceTier struct {
	InputTokensAbove int      `json:"input_tokens_above"`
	Input            *float64 `json:"input"`
	Output           *float64 `json:"output"`
	CacheRead        *float64 `json:"cache_read"`
	CacheWrite       *float64 `json:"cache_write"`
}

// EntryCache is an entry's prompt caching and its two profiles.
type EntryCache struct {
	Documented string   `json:"documented"`
	Retention  string   `json:"retention"`
	Short      Lifetime `json:"short"`
	Long       Lifetime `json:"long"`
}

// Lifetime is one profile: the lifetime to rely on by the rule of 4.2, and what it rests on.
type Lifetime struct {
	RelyOnS          *int               `json:"rely_on_s"`
	Basis            string             `json:"basis"`
	Date             *string            `json:"date"`
	Safe             SafeLifetime       `json:"safe"`
	DocumentedS      *int               `json:"documented_s"`
	DocumentedStatus *string            `json:"documented_status"`
	ThroughProvimux  []MeasuredLifetime `json:"through_provimux"`
}

// SafeLifetime is providers.dev's own safe value of a profile.
type SafeLifetime struct {
	Seconds      *int            `json:"seconds"`
	Basis        string          `json:"basis"`
	Source       *string         `json:"source"`
	Date         *string         `json:"date"`
	MeasuredWith []FiguresClient `json:"measured_with"`
}

// FiguresClient names a client providers.dev measured with.
type FiguresClient struct {
	Client string  `json:"client"`
	Label  *string `json:"label"`
	Kind   *string `json:"kind"`
}

// MeasuredLifetime is one measurement through Provimux: openapi.yaml, components/schemas/Measurement.
type MeasuredLifetime struct {
	Client     string  `json:"client"`
	Label      *string `json:"label"`
	Counts     bool    `json:"counts"`
	LifetimeS  int     `json:"lifetime_s"`
	Date       *string `json:"date"`
	Checks     int     `json:"checks"`
	Hits       int     `json:"hits"`
	FirstMissS *int    `json:"first_miss_s"`
	Asked      *Asked  `json:"asked"`
}

// Asked is the request field that asked for a lifetime.
type Asked struct {
	Parameter string `json:"parameter"`
	Value     string `json:"value"`
}

// FiguresLimit is a window or an output limit in tokens.
type FiguresLimit struct {
	Tokens  *int    `json:"tokens"`
	AtLeast bool    `json:"at_least"`
	Source  *string `json:"source"`
	Date    *string `json:"date"`
}

// ---- the rule of prices-and-cache.md 4.2

// Basis values of rely_on_s.
const (
	BasisMeasuredThroughProvimux = "measured_through_provimux"
	BasisMeasured                = "measured"
	BasisDocumented              = "documented"
	BasisNone                    = "none"
)

// The providers.dev client ids of measurements through Provimux (4.1, 4.2): a real client, a coding agent
// holding a conversation, and the plain probe. Only the first counts for rely_on_s.
const (
	clientRealThroughProvimux  = "agent+cliproxyapi"
	clientProbeThroughProvimux = "cliproxyapi"
)

// CountsForRelyOn reports whether a measurement by this providers.dev client id is a real client's through
// Provimux (prices-and-cache.md 4.2): today only "agent+cliproxyapi". A plain probe's ("cliproxyapi") never
// counts.
func CountsForRelyOn(client string) bool { return client == clientRealThroughProvimux }

func throughProvimux(client string) bool {
	return client == clientRealThroughProvimux || client == clientProbeThroughProvimux
}

// RelyOn applies prices-and-cache.md 4.2 to one profile of an entry: the lowest measurement that counts (of
// equal ones the newest), else the safe value by its basis (measured, then documented), else nothing.
func RelyOn(safeSeconds *int, safeBasis string, safeDate *string, measured []MeasuredLifetime) (*int, string, *string) {
	var best *MeasuredLifetime
	for i := range measured {
		m := &measured[i]
		if !m.Counts {
			continue
		}
		if best == nil || m.LifetimeS < best.LifetimeS || (m.LifetimeS == best.LifetimeS && dayAfter(m.Date, best.Date)) {
			best = m
		}
	}
	if best != nil {
		return intPtr(best.LifetimeS), BasisMeasuredThroughProvimux, copyString(best.Date)
	}
	if safeSeconds != nil && (safeBasis == BasisMeasured || safeBasis == BasisDocumented) {
		return intPtr(*safeSeconds), safeBasis, copyString(safeDate)
	}
	return nil, BasisNone, nil
}

// dayAfter reports whether day a is after day b; a known day is after an unknown one.
func dayAfter(a, b *string) bool {
	switch {
	case a == nil:
		return false
	case b == nil:
		return true
	}
	return *a > *b // YYYY-MM-DD compares as text
}

// ---- reading an entry summary (GET /api/v1/entries, EntrySummary of API 1.6)

// isDay reports whether value is written YYYY-MM-DD.
func isDay(value string) bool {
	if len(value) != 10 || value[4] != '-' || value[7] != '-' {
		return false
	}
	for i, c := range value {
		if i != 4 && i != 7 && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

type summaryJSON struct {
	Ref            *string        `json:"ref"`
	Provider       *string        `json:"provider"`
	Model          *string        `json:"model"`
	Access         *string        `json:"access"`
	PriceIn        *float64       `json:"price_in"`
	PriceOut       *float64       `json:"price_out"`
	PriceFrom      *string        `json:"price_from"`
	Pricing        *pricingJSON   `json:"pricing"`
	Safe           *safeJSON      `json:"safe"`
	Cache          *string        `json:"cache"`
	CacheRetention *string        `json:"cache_retention"`
	CacheLifetime  *lifetimesJSON `json:"cache_lifetime"`
}

type pricingJSON struct {
	Billing      *string           `json:"billing"`
	ReferenceRef *string           `json:"reference_ref"`
	Rules        []json.RawMessage `json:"rules"`
}

type ruleJSON struct {
	When      json.RawMessage `json:"when"`
	Meter     string          `json:"meter"`
	Operation string          `json:"operation"`
	Amount    *float64        `json:"amount"`
	Currency  *string         `json:"currency"`
	Unit      *struct {
		Name     string   `json:"name"`
		Quantity *float64 `json:"quantity"`
	} `json:"unit"`
}

type conditionJSON struct {
	Kind       string            `json:"kind"`
	Field      string            `json:"field"`
	Op         string            `json:"op"`
	Value      json.RawMessage   `json:"value"`
	Conditions []json.RawMessage `json:"conditions"`
}

type safeJSON struct {
	InputWindow *safeValueJSON `json:"input_window"`
	MaxOutput   *safeValueJSON `json:"max_output"`
	Price       *safePriceJSON `json:"price"`
}

type safeValueJSON struct {
	Value   *float64 `json:"value"`
	AtLeast bool     `json:"at_least"`
	Source  *string  `json:"source"`
	Date    *string  `json:"date"`
}

type safePriceJSON struct {
	Source    *string `json:"source"`
	Date      *string `json:"date"`
	Agreement *string `json:"agreement"`
	Sources   []struct {
		Source string `json:"source"`
		Tiers  []struct {
			InputTokensAbove *float64 `json:"input_tokens_above"`
			Input            *float64 `json:"input"`
			Output           *float64 `json:"output"`
			CacheRead        *float64 `json:"cache_read"`
			CacheWrite       *float64 `json:"cache_write"`
		} `json:"tiers"`
	} `json:"sources"`
}

type lifetimesJSON struct {
	Short     *safeLifetimeJSON  `json:"short"`
	Long      *safeLifetimeJSON  `json:"long"`
	Lifetimes []lifetimeItemJSON `json:"lifetimes"`
}

type safeLifetimeJSON struct {
	SafeS            *int    `json:"safe_s"`
	Basis            *string `json:"basis"`
	Source           *string `json:"source"`
	Date             *string `json:"date"`
	DocumentedS      *int    `json:"documented_s"`
	DocumentedStatus *string `json:"documented_status"`
}

type lifetimeItemJSON struct {
	RetentionProfile string  `json:"retention_profile"`
	Basis            string  `json:"basis"`
	Date             *string `json:"date"`
	LifetimeS        *int    `json:"lifetime_s"` // missing or null: no measurement
	Client           *string `json:"client"`
	ClientLabel      *string `json:"client_label"`
	ClientKind       *string `json:"client_kind"`
	Parameter        *string `json:"parameter"`
	Value            *string `json:"value"`
	Checks           int     `json:"checks"`
	Hits             int     `json:"hits"`
	FirstMissS       *int    `json:"first_miss_s"`
	Chosen           bool    `json:"chosen"`
}

// ReadEntrySummary turns one entry summary into the kept record. An error means Provimux cannot read the
// entry: it is left out, and the others are kept.
func ReadEntrySummary(raw []byte) (Figures, error) {
	var s summaryJSON
	if err := json.Unmarshal(raw, &s); err != nil {
		return Figures{}, fmt.Errorf("entry: %w", err)
	}
	provider, model := trimmed(s.Provider), trimmed(s.Model)
	switch {
	case provider == "" || model == "" || strings.Contains(provider, "/"):
		return Figures{}, errors.New("entry: no provider or model")
	case trimmed(s.Ref) != EntryRef(provider, model):
		return Figures{}, errors.New("entry: ref is not provider/model")
	case s.Pricing == nil || s.Safe == nil || s.Safe.InputWindow == nil || s.Safe.MaxOutput == nil || s.Safe.Price == nil:
		return Figures{}, errors.New("entry: pricing or safe missing")
	case s.CacheLifetime == nil || s.CacheLifetime.Short == nil || s.CacheLifetime.Long == nil:
		return Figures{}, errors.New("entry: cache_lifetime missing")
	}
	f := Figures{
		Provider:    provider,
		Model:       model,
		URL:         EntryPageURL(provider, model),
		Access:      oneOf(s.Access, "unknown", "api", "subscription", "unknown"),
		Price:       readPrice(&s),
		InputWindow: readLimit(s.Safe.InputWindow),
		MaxOutput:   readLimit(s.Safe.MaxOutput),
	}
	f.Cache = EntryCache{
		Documented: oneOf(s.Cache, "unknown", "yes", "no", "unknown"),
		Retention:  oneOf(s.CacheRetention, "unknown", "supported", "unsupported", "unknown"),
		Short:      readLifetime("short", s.CacheLifetime.Short, s.CacheLifetime.Lifetimes),
		Long:       readLifetime("long", s.CacheLifetime.Long, s.CacheLifetime.Lifetimes),
	}
	return f, nil
}

func readPrice(s *summaryJSON) EntryPrice {
	p := EntryPrice{CacheWrite: []CacheWriteRate{}, Tiers: []PriceTier{}}
	switch billing := trimmed(s.Pricing.Billing); billing {
	case "per_use", "reference":
		p.Billing = billing
	default: // not_applicable, unknown, or a value this reader does not know
		p.Billing = "unknown"
	}
	safe := s.Safe.Price
	p.Source, p.Date = copyString(safe.Source), day(safe.Date)
	p.SourcesDiffer = safe.Agreement != nil && *safe.Agreement == "differ"
	if p.Billing == "unknown" {
		// No price applies or none is known: every price is null.
		return p
	}
	if p.Billing == "reference" {
		reference := s.PriceFrom
		if reference == nil {
			reference = s.Pricing.ReferenceRef
		}
		if reference != nil && !isEntryRef(*reference) {
			// Not a reference the contract can carry: the entry's price is left out.
			p.Billing = "unknown"
			return p
		}
		p.Reference = copyString(reference)
	}
	p.Input, p.Output = copyFloat(s.PriceIn), copyFloat(s.PriceOut)
	if safe.Source != nil {
		for _, source := range safe.Sources {
			if source.Source != *safe.Source {
				continue
			}
			for _, tier := range source.Tiers {
				if tier.InputTokensAbove == nil || math.Abs(math.Round(*tier.InputTokensAbove)) > maxFiguresTokens {
					continue // no size, or one too large to carry: the tier is left out
				}
				p.Tiers = append(p.Tiers, PriceTier{InputTokensAbove: int(math.Round(*tier.InputTokensAbove)),
					Input: copyFloat(tier.Input), Output: copyFloat(tier.Output), CacheRead: copyFloat(tier.CacheRead), CacheWrite: copyFloat(tier.CacheWrite)})
			}
			break
		}
	}
	sort.SliceStable(p.Tiers, func(i, j int) bool { return p.Tiers[i].InputTokensAbove < p.Tiers[j].InputTokensAbove })
	tierAt := map[int]bool{}
	for _, tier := range p.Tiers {
		tierAt[tier.InputTokensAbove] = true
	}
	for _, raw := range s.Pricing.Rules {
		var rule ruleJSON
		if err := json.Unmarshal(raw, &rule); err != nil {
			p.OtherRates = true // a rule this reader cannot read: a rate the record does not carry
			continue
		}
		if rule.Meter == "text.cache_write" && rule.Amount != nil && !validPrice(*rule.Amount) {
			continue // a negative cache write price is left out, as a negative cache read price is
		}
		conditional := len(rule.When) > 0 && string(rule.When) != "null"
		basePrice := rule.Operation == "set" && perMillionUSD(&rule)
		switch {
		case !conditional && basePrice && rule.Meter == "text.cache_read":
			if p.CacheRead == nil {
				p.CacheRead = copyFloat(rule.Amount)
			}
		case !conditional && basePrice && rule.Meter == "text.cache_write":
			p.CacheWrite = append(p.CacheWrite, CacheWriteRate{Price: *rule.Amount})
		case !conditional:
			// Base input and output come from price_in and price_out; other unconditional rules (web search, ...)
			// are not rates of this record and are no "other rate" either (4.1: any other rule with a when).
		case basePrice && rule.Meter == "text.cache_write":
			if lifetime, ok := retentionCondition(rule.When); ok {
				p.CacheWrite = append(p.CacheWrite, CacheWriteRate{LifetimeS: intPtr(lifetime), Price: *rule.Amount})
			} else {
				p.OtherRates = p.OtherRates || !carriedByTier(rule.When, tierAt)
			}
		default:
			p.OtherRates = p.OtherRates || !carriedByTier(rule.When, tierAt)
		}
	}
	sort.SliceStable(p.CacheWrite, func(i, j int) bool {
		a, b := p.CacheWrite[i].LifetimeS, p.CacheWrite[j].LifetimeS
		switch {
		case a == nil:
			return b != nil
		case b == nil:
			return false
		}
		return *a < *b
	})
	return p
}

// perMillionUSD reports whether a rule's amount is US dollars per million tokens.
func perMillionUSD(rule *ruleJSON) bool {
	return rule.Amount != nil && rule.Currency != nil && strings.EqualFold(*rule.Currency, "USD") &&
		rule.Unit != nil && rule.Unit.Name == "token" && rule.Unit.Quantity != nil && *rule.Unit.Quantity == 1e6
}

// retentionCondition reads a condition "cache.retention_s eq N" with a whole N above 0.
func retentionCondition(raw json.RawMessage) (int, bool) {
	var c conditionJSON
	if json.Unmarshal(raw, &c) != nil || c.Kind != "predicate" || c.Field != "cache.retention_s" || c.Op != "eq" {
		return 0, false
	}
	var value float64
	if json.Unmarshal(c.Value, &value) != nil || value <= 0 || value != math.Trunc(value) {
		return 0, false
	}
	return int(value), true
}

// carriedByTier reports whether a conditional rule is a price above a prompt size that the record's tiers
// carry: its condition is only "request.context_tokens (or request.input_tokens) gt N" at a tier's N.
func carriedByTier(raw json.RawMessage, tierAt map[int]bool) bool {
	var c conditionJSON
	if json.Unmarshal(raw, &c) != nil || c.Kind != "predicate" || c.Op != "gt" ||
		(c.Field != "request.context_tokens" && c.Field != "request.input_tokens") {
		return false
	}
	var value float64
	if json.Unmarshal(c.Value, &value) != nil || value != math.Trunc(value) {
		return false
	}
	return tierAt[int(value)]
}

func readLimit(v *safeValueJSON) FiguresLimit {
	limit := FiguresLimit{AtLeast: v.AtLeast, Source: copyString(v.Source), Date: day(v.Date)}
	if tokens, ok := wholeTokens(v.Value); ok {
		limit.Tokens = intPtr(tokens)
	} else {
		limit.AtLeast = false
	}
	return limit
}

// wholeTokens reads a token count: rounded to a whole number, never below 0 and never above maxFiguresTokens.
// Anything else is no count (left out, never clamped).
func wholeTokens(value *float64) (int, bool) {
	if value == nil {
		return 0, false
	}
	rounded := math.Round(*value)
	if math.IsNaN(rounded) || rounded < 0 || rounded > maxFiguresTokens || rounded > float64(math.MaxInt) {
		return 0, false
	}
	return int(rounded), true
}

func readLifetime(profile string, safe *safeLifetimeJSON, items []lifetimeItemJSON) Lifetime {
	out := Lifetime{
		Safe: SafeLifetime{
			Basis:        oneOf(safe.Basis, BasisNone, BasisMeasured, BasisDocumented, BasisNone),
			Source:       copyString(safe.Source),
			Date:         day(safe.Date),
			MeasuredWith: []FiguresClient{},
		},
		DocumentedS:      copyNonNegative(safe.DocumentedS),
		DocumentedStatus: copyString(safe.DocumentedStatus),
		ThroughProvimux:  []MeasuredLifetime{},
	}
	if out.Safe.Basis != BasisNone {
		out.Safe.Seconds = copyNonNegative(safe.SafeS)
	}
	if out.Safe.Seconds == nil {
		out.Safe.Basis = BasisNone
	}
	seen := map[string]bool{}
	for _, item := range items {
		if item.RetentionProfile != profile || item.Basis != "measured" || item.Client == nil || item.LifetimeS == nil || *item.LifetimeS < 0 {
			continue
		}
		client := *item.Client
		// An empty client id names no client: left out here, as the disk copy's reading refuses it (Lifetime.check).
		if item.Chosen && out.Safe.Basis == BasisMeasured && client != "" && !seen[client] {
			seen[client] = true
			out.Safe.MeasuredWith = append(out.Safe.MeasuredWith, FiguresClient{Client: client,
				Label: copyString(item.ClientLabel), Kind: copyString(item.ClientKind)})
		}
		if !throughProvimux(client) {
			continue
		}
		m := MeasuredLifetime{Client: client, Label: copyString(item.ClientLabel), Counts: CountsForRelyOn(client),
			LifetimeS: *item.LifetimeS, Date: day(item.Date), Checks: item.Checks, Hits: item.Hits,
			FirstMissS: copyNonNegative(item.FirstMissS)}
		if item.Parameter != nil && item.Value != nil {
			m.Asked = &Asked{Parameter: *item.Parameter, Value: *item.Value}
		}
		out.ThroughProvimux = append(out.ThroughProvimux, m)
	}
	// Newest first; of one day, as providers.dev lists them.
	sort.SliceStable(out.ThroughProvimux, func(i, j int) bool {
		return dayAfter(out.ThroughProvimux[i].Date, out.ThroughProvimux[j].Date)
	})
	out.RelyOnS, out.Basis, out.Date = RelyOn(out.Safe.Seconds, out.Safe.Basis, out.Safe.Date, out.ThroughProvimux)
	return out
}

// ---- the document, its pages and the fetch

// FiguresPage is what Provimux keeps of one page: enough to send If-None-Match and to reuse the page on 304.
type FiguresPage struct {
	Offset     int      `json:"offset"`
	ETag       string   `json:"etag"`
	NextOffset *int     `json:"next_offset"`
	Count      int      `json:"count"` // entries on the page, read or not
	Refs       []string `json:"refs"`  // the entries kept from it
}

// FiguresDocument is an accepted answer, as held in memory and written to figures.json. It is never changed
// once made: a new answer makes a new document.
type FiguresDocument struct {
	Schema         int                `json:"schema"`
	Providers      string             `json:"providers"` // the request's provider filter: its ETags are for it only
	DatasetVersion string             `json:"dataset_version"`
	GeneratedAt    time.Time          `json:"generated_at"`
	FetchedAt      time.Time          `json:"fetched_at"`
	Total          int                `json:"total"`
	Pages          []FiguresPage      `json:"pages"`
	Entries        map[string]Figures `json:"entries"`
	Unread         int                `json:"unread"` // entries of the pages Provimux could not read
}

// WithFetchedAt is the document answered again unchanged (every page 304) at t.
func (d *FiguresDocument) WithFetchedAt(t time.Time) *FiguresDocument {
	copied := *d
	copied.FetchedAt = t.UTC()
	return &copied
}

// FiguresResult is a fetch that got an answer: every page 304 (NotModified), or a document to accept.
type FiguresResult struct {
	NotModified bool
	Document    *FiguresDocument
}

type figuresPageJSON struct {
	Meta *struct {
		DatasetVersion string `json:"dataset_version"`
		GeneratedAt    string `json:"generated_at"`
	} `json:"meta"`
	Total      *int              `json:"total"`
	Offset     *int              `json:"offset"`
	NextOffset *int              `json:"next_offset"`
	Data       []json.RawMessage `json:"data"`
}

// FetchFigures asks every page, each within pageTimeout. held is the held document (nil without one). now is
// the gateway's clock. A result with NotModified means every page answered 304: the held document is current.
func FetchFigures(ctx context.Context, client *http.Client, base string, held *FiguresDocument, userAgent string,
	now time.Time, pageTimeout time.Duration) (FiguresResult, error) {
	if client == nil {
		client = http.DefaultClient
	}
	// The caller's client (its proxy, its transport) with this request's own rule for redirects.
	following := *client
	following.CheckRedirect = figuresRedirect
	client = &following
	providers := strings.Join(FigureProviders(), ",")
	heldPages := map[int]*FiguresPage{}
	if held != nil && held.Providers == providers {
		// An ETag is sent back only to the URL it came from: never after the fixed set changed.
		for i := range held.Pages {
			heldPages[held.Pages[i].Offset] = &held.Pages[i]
		}
	}
	doc := &FiguresDocument{Schema: FiguresSchema, Providers: providers, FetchedAt: now.UTC(), Entries: map[string]Figures{}}
	allNotModified, count := true, 0
	offset := 0
	for i := 0; ; i++ {
		if i == FiguresMaxPages {
			return FiguresResult{}, failure(FailureResponse, fmt.Errorf("more than %d pages", FiguresMaxPages))
		}
		heldPage := heldPages[offset]
		etag := ""
		if heldPage != nil {
			etag = heldPage.ETag
		}
		status, body, respETag, err := getFiguresPage(ctx, client, FiguresRequestURL(base, offset), etag, userAgent, pageTimeout)
		if err != nil {
			return FiguresResult{}, err
		}
		var page FiguresPage
		var version string
		var generated time.Time
		var total int
		switch {
		case status == http.StatusNotModified && etag != "":
			// The page is the held one, byte for byte: its entries are the held ones.
			page = *heldPage
			version, generated, total = held.DatasetVersion, held.GeneratedAt, held.Total
			for _, ref := range page.Refs {
				entry, ok := held.Entries[ref]
				if !ok {
					return FiguresResult{}, failure(FailureResponse, errors.New("a held page names an entry not held"))
				}
				if _, dup := doc.Entries[ref]; dup {
					return FiguresResult{}, failure(FailureResponse, fmt.Errorf("entry %s on two pages", ref))
				}
				doc.Entries[ref] = entry
			}
			doc.Unread += page.Count - len(page.Refs)
		case status == http.StatusOK:
			allNotModified = false
			var parsed *figuresPageParsed
			parsed, err = parseFiguresPage(body, offset)
			if err != nil {
				return FiguresResult{}, failure(FailureResponse, err)
			}
			version, generated, total = parsed.version, parsed.generated, parsed.total
			page = FiguresPage{Offset: offset, ETag: respETag, NextOffset: parsed.next, Count: parsed.count, Refs: []string{}}
			for _, entry := range parsed.entries {
				ref := EntryRef(entry.Provider, entry.Model)
				if _, dup := doc.Entries[ref]; dup {
					return FiguresResult{}, failure(FailureResponse, fmt.Errorf("entry %s on two pages", ref))
				}
				doc.Entries[ref] = entry
				page.Refs = append(page.Refs, ref)
			}
			doc.Unread += parsed.count - len(parsed.entries)
		default:
			return FiguresResult{}, &FetchError{Category: FailureHTTP, Status: status}
		}
		if i == 0 {
			doc.DatasetVersion, doc.GeneratedAt, doc.Total = version, generated, total
			if generated.After(now.Add(FiguresMaxAhead)) {
				return FiguresResult{}, failure(FailureResponse, ErrAhead)
			}
		} else if version != doc.DatasetVersion || !generated.Equal(doc.GeneratedAt) || total != doc.Total {
			return FiguresResult{}, failure(FailureResponse, errors.New("the pages are of two datasets"))
		}
		doc.Pages = append(doc.Pages, page)
		count += page.Count
		if page.NextOffset == nil {
			break
		}
		if *page.NextOffset <= offset {
			return FiguresResult{}, failure(FailureResponse, errors.New("next_offset does not move on"))
		}
		offset = *page.NextOffset
	}
	if count != doc.Total {
		return FiguresResult{}, failure(FailureResponse, fmt.Errorf("incomplete answer: %d of %d entries", count, doc.Total))
	}
	if len(doc.Entries) == 0 {
		// Never replaces a held document, and is no document to hold (decided by the lead on 2026-11-27).
		return FiguresResult{}, failure(FailureResponse, ErrNoEntries)
	}
	if allNotModified && held != nil {
		return FiguresResult{NotModified: true}, nil
	}
	return FiguresResult{Document: doc}, nil
}

// figuresRedirect follows a redirect only to the same host and port over https; any other answers as the
// redirect itself, a status the fetch does not accept.
func figuresRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	if len(via) >= maxFiguresRedirects || req.URL.Scheme != "https" || !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
		return http.ErrUseLastResponse
	}
	return nil
}

func getFiguresPage(ctx context.Context, client *http.Client, pageURL, etag, userAgent string, timeout time.Duration) (int, []byte, string, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return 0, nil, "", failure(FailureNetwork, err)
	}
	req.Header.Set("User-Agent", userAgent)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, "", failure(FailureNetwork, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil, "", nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFiguresPageBytes+1))
	if err != nil {
		// A body cut off in transfer: the answer never arrived whole.
		return 0, nil, "", failure(FailureNetwork, err)
	}
	if len(body) > maxFiguresPageBytes {
		return 0, nil, "", failure(FailureResponse, errors.New("page larger than 8 MB"))
	}
	return resp.StatusCode, body, strings.TrimSpace(resp.Header.Get("ETag")), nil
}

type figuresPageParsed struct {
	version   string
	generated time.Time
	total     int
	next      *int
	count     int
	entries   []Figures
}

// parseFiguresPage reads one page of GET /api/v1/entries. The envelope must be whole; each entry is read on
// its own, and one Provimux cannot read, or of a provider it did not ask for, is left out alone.
func parseFiguresPage(body []byte, offset int) (*figuresPageParsed, error) {
	var page figuresPageJSON
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("malformed answer: %w", err)
	}
	if page.Meta == nil || page.Data == nil || page.Total == nil || page.Offset == nil {
		return nil, errors.New("malformed answer: meta, total, offset or data missing")
	}
	version := strings.TrimSpace(page.Meta.DatasetVersion)
	generated, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(page.Meta.GeneratedAt))
	if err != nil || version == "" {
		return nil, errors.New("malformed answer: dataset_version or generated_at")
	}
	if *page.Offset != offset || *page.Total < 0 {
		return nil, fmt.Errorf("malformed answer: offset %d for %d", *page.Offset, offset)
	}
	parsed := &figuresPageParsed{version: version, generated: generated.UTC(), total: *page.Total, next: page.NextOffset, count: len(page.Data)}
	for _, raw := range page.Data {
		entry, errEntry := ReadEntrySummary(raw)
		if errEntry != nil || !isFigureProvider(entry.Provider) {
			continue
		}
		parsed.entries = append(parsed.entries, entry)
	}
	return parsed, nil
}

// AcceptFigures decides whether a fetched document replaces the held one (nil without one): not when it was
// generated before the held one (ErrOlder).
func AcceptFigures(held, fetched *FiguresDocument) (*FiguresDocument, error) {
	if fetched == nil {
		return held, nil
	}
	if held != nil && fetched.GeneratedAt.Before(held.GeneratedAt) {
		return held, ErrOlder
	}
	return fetched, nil
}

// ---- the disk copy

// ReadFigures returns the disk copy of the last accepted figures, read at the gateway's clock now. It first
// removes what an interrupted write left (.figures.json.*.tmp). ErrNotCached without a copy; any other error
// means the file was damaged or foreign and was ignored (warned once). The file is the gateway's own: its
// entries are not read again as a fetched page's are. A copy generated more than FiguresMaxAhead ahead of now
// is ignored.
func (s *Store) ReadFigures(now time.Time) (*FiguresDocument, error) {
	if s == nil || s.dir == "" {
		return nil, ErrNotCached
	}
	s.removeFiguresLeftovers()
	file, err := os.Open(filepath.Join(s.dir, FiguresFileName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotCached
		}
		s.warnOnce("read:"+FiguresFileName, "model lists cache: cannot read %s: %v", FiguresFileName, err)
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxFiguresPageBytes+1))
	if err == nil && len(data) > maxFiguresPageBytes {
		err = fmt.Errorf("larger than %d bytes", maxFiguresPageBytes)
	}
	var doc FiguresDocument
	if err == nil {
		err = json.Unmarshal(data, &doc)
	}
	if err == nil {
		err = doc.check(now)
	}
	if err != nil {
		s.warnOnce("bad:"+FiguresFileName, "model lists cache: ignoring %s (%v); the next accepted document replaces it", FiguresFileName, err)
		return nil, err
	}
	return &doc, nil
}

// removeFiguresLeftovers removes the temporary files of an interrupted write of figures.json.
func (s *Store) removeFiguresLeftovers() {
	leftovers, _ := filepath.Glob(filepath.Join(s.dir, "."+FiguresFileName+".*.tmp"))
	for _, leftover := range leftovers {
		_ = os.Remove(leftover)
	}
}

func (d *FiguresDocument) check(now time.Time) error {
	switch {
	case d.Schema != FiguresSchema:
		return fmt.Errorf("schema %d", d.Schema)
	case strings.TrimSpace(d.DatasetVersion) == "":
		return errors.New("no dataset version")
	case d.GeneratedAt.IsZero() || d.FetchedAt.IsZero():
		return errors.New("no dates")
	case d.GeneratedAt.After(now.Add(FiguresMaxAhead)):
		return ErrAhead
	case len(d.Pages) == 0 || len(d.Pages) > FiguresMaxPages:
		return errors.New("no pages")
	case len(d.Entries) == 0:
		return errors.New("no entries")
	}
	refs := 0
	for _, page := range d.Pages {
		for _, ref := range page.Refs {
			if _, ok := d.Entries[ref]; !ok {
				return fmt.Errorf("page %d names %q, which is not held", page.Offset, ref)
			}
		}
		refs += len(page.Refs)
	}
	if refs != len(d.Entries) {
		return errors.New("entries outside the pages")
	}
	for ref, entry := range d.Entries {
		if ref != EntryRef(entry.Provider, entry.Model) || !isFigureProvider(entry.Provider) || entry.Model == "" {
			return fmt.Errorf("entry %q is not valid", ref)
		}
	}
	return nil
}

// WriteFigures stores the accepted figures: mode 0600, a temporary file renamed over the old one.
func (s *Store) WriteFigures(doc *FiguresDocument) error {
	if s == nil || s.dir == "" || doc == nil {
		return nil
	}
	data, err := json.Marshal(doc)
	if err == nil {
		err = writeFileAtomic(s.dir, FiguresFileName, data)
	}
	if err != nil {
		s.warnOnce("write", "model lists cache: cannot write to %s (%v); lists stay in memory only", s.dir, err)
		return err
	}
	s.clearWarning("bad:" + FiguresFileName)
	s.clearWarning("write")
	return nil
}

// ---- small helpers

// isEntryRef reports whether value is of the contract's EntryRef form: "<provider>/<model>", a provider
// without a slash and a model of at least one character.
func isEntryRef(value string) bool {
	slash := strings.IndexByte(value, '/')
	return slash > 0 && slash < len(value)-1 && !strings.ContainsAny(value, "\n\r")
}

func validPrice(value float64) bool {
	return value >= 0 && !math.IsInf(value, 0) && !math.IsNaN(value)
}

func trimmed(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

// oneOf returns the value when it is one of allowed, else fallback.
func oneOf(value *string, fallback string, allowed ...string) string {
	v := trimmed(value)
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return fallback
}

func day(value *string) *string {
	if value == nil || !isDay(*value) {
		return nil
	}
	return copyString(value)
}

func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	v := *value
	return &v
}

func copyFloat(value *float64) *float64 {
	if value == nil || *value < 0 || math.IsNaN(*value) || math.IsInf(*value, 0) {
		return nil
	}
	v := *value
	return &v
}

func copyNonNegative(value *int) *int {
	if value == nil || *value < 0 {
		return nil
	}
	v := *value
	return &v
}

func intPtr(v int) *int { return &v }
