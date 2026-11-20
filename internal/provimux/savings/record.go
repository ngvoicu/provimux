package savings

import (
	"context"
	"math"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// sample is what is kept of one usage record (01-recording.md sections 2 to 4).
type sample struct {
	day            string
	kind           string
	provider, host string
	model, key     string
	n              counters
}

// sampleOf reduces a record to what is stored. Pure. Nothing else of the record is read: not Alias,
// Source, AuthID, AuthIndex, SessionID, ResponseHeaders or Fail.Body.
func sampleOf(ctx context.Context, record coreusage.Record, location *time.Location, now time.Time) sample {
	s := sample{
		kind:     kindOf(record.AuthType),
		provider: cut(record.Provider, 64),
		model:    cut(record.Model, 128),
		key:      record.APIKey,
	}
	if strings.TrimSpace(s.key) == "" {
		s.key = ""
	}
	if s.kind == "apikey" {
		s.host = hostOf(record.BaseURL)
	}
	at := record.RequestedAt
	if at.IsZero() {
		at = now
	}
	s.day = at.In(location).Format(time.DateOnly)

	s.n.Req = 1
	if record.Failed || internallogging.GetResponseStatus(ctx) >= 400 {
		s.n.Fail = 1
	}

	// Tokens come from the normalised breakdown only (D8): providers disagree on what input means,
	// and Claude's CachedTokens falls back to cache writes.
	detail := coreusage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)
	b := detail.TokenBreakdown
	s.n.In = nonNegative(b.Input.UncachedTokens)
	s.n.Cr = nonNegative(b.Input.CacheReadTokens)
	s.n.Cw = nonNegative(b.Input.CacheWriteTokens)
	s.n.Out = nonNegative(b.Output.TotalTokens)
	s.n.Rsn = nonNegative(b.Output.ReasoningTokens)
	s.n.Unc = nonNegative(b.UnclassifiedTokens)
	cw5, cw1h := deref(detail.CacheCreation5mTokens), deref(detail.CacheCreation1hTokens)
	if cw5+cw1h <= s.n.Cw {
		s.n.Cw5, s.n.Cw1h = cw5, cw1h
	}

	if costs := detail.CostDetails; costs != nil {
		if costs.Cost != nil && *costs.Cost >= 0 && !math.IsInf(*costs.Cost, 0) {
			s.n.Cost = int64(math.Round(*costs.Cost * 1e9))
			s.n.Costn = 1
		}
		if costs.IsBYOK != nil && *costs.IsBYOK && costs.UpstreamInferenceCost != nil &&
			*costs.UpstreamInferenceCost >= 0 && !math.IsInf(*costs.UpstreamInferenceCost, 0) {
			s.n.Ucost = int64(math.Round(*costs.UpstreamInferenceCost * 1e9))
		}
	}
	return s
}

func kindOf(authType string) string {
	switch kind := strings.ToLower(strings.TrimSpace(authType)); kind {
	case "oauth", "apikey":
		return kind
	default:
		return "unknown"
	}
}

// hostOf is the rule of provimuxBaseURLHost: the lower-cased hostname, never the path, the port or
// user info.
func hostOf(baseURL string) string {
	parsed, errParse := url.Parse(strings.TrimSpace(baseURL))
	if errParse != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if len(host) > 255 {
		return ""
	}
	return host
}

// cut trims, lower-cases and cuts at limit bytes, on a character boundary. Empty is "unknown".
func cut(value string, limit int) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) > limit {
		end := limit
		for end > 0 && !utf8.RuneStart(value[end]) {
			end--
		}
		value = value[:end]
	}
	if value == "" {
		return "unknown"
	}
	return value
}

func nonNegative(v int64) int64 { return max(v, 0) }

func deref(v *int64) int64 {
	if v == nil {
		return 0
	}
	return nonNegative(*v)
}

// HandleUsage implements coreusage.Plugin. No I/O: it adds the record to the incoming deltas under
// inMu, a lock nothing holds across a store call, and returns.
func (r *recorder) HandleUsage(ctx context.Context, record coreusage.Record) {
	s := sampleOf(ctx, record, r.location(), r.opts.now())
	// The account's provider, not the executor's: Kimi's Claude Messages route runs the embedded
	// Claude executor. The auth ID is only looked up, never kept.
	if provider := r.opts.accountProvider(record.AuthID); provider != "" {
		s.provider = cut(provider, 64)
	}
	pk := pendingKey{k: s.kind, p: s.provider, h: s.host, m: s.model, key: s.key}
	r.inMu.Lock()
	d := r.incoming[s.day]
	if d == nil {
		d = newDeltaDay()
		r.incoming[s.day] = d
	}
	d.add(pk, s.n)
	r.inMu.Unlock()
}
