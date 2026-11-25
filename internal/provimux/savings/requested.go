package savings

import (
	"maps"
	"slices"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// Requests by the model name the client sent (only-my-names round 2). The rows count a request under
// the model the account ran (an alias's traffic under the vendor's model, without the account's
// prefix); the console's "used in the last 30 days" must count what the owner's tools send, so each
// day also keeps, apart from its rows, the requests and tokens per requested name. The rows, their
// limit and every number the Savings page shows are unchanged.

// requestedName is the name the client sent (Record.Alias: the executor's requested-model alias, the
// model itself when there is none), without a thinking suffix, trimmed and cut to a row's length, in
// its own case: only-my-names compares names exactly, so "keep all used" must keep what was sent
// (B-Coder, not b-coder). Days recorded before this change hold the names in lower case.
func requestedName(record coreusage.Record) string {
	name := strings.TrimSpace(record.Alias)
	if name == "" {
		return ""
	}
	if base := strings.TrimSpace(thinking.ParseSuffix(name).ModelName); base != "" {
		name = base
	}
	return cutKeepingCase(name, 128)
}

// askedCounters keeps of a request's counters what the requested-name tally needs.
func askedCounters(n counters) counters {
	return counters{Req: n.Req, Fail: n.Fail, In: n.In + n.Cr + n.Cw, Out: n.Out}
}

// addAsked counts n under a requested name; beyond maxRows names a day's new names count as ~other.
func (d *deltaDay) addAsked(name string, n counters) {
	if d.asked == nil {
		d.asked = map[string]*counters{}
	}
	addAskedTo(d.asked, name, askedCounters(n))
}

func addAskedTo(asked map[string]*counters, name string, n counters) {
	if c, ok := asked[name]; ok {
		c.add(n)
		return
	}
	if len(asked) >= maxRows && name != otherValue {
		addAskedTo(asked, otherValue, n)
		return
	}
	c := n
	asked[name] = &c
}

// mergeAsked adds requested-name counts to the day.
func (d *dayRows) mergeAsked(asked map[string]*counters) {
	if d.asked == nil {
		d.asked = map[string]*counters{}
	}
	for _, name := range slices.Sorted(maps.Keys(asked)) {
		addAskedTo(d.asked, name, *asked[name])
	}
}

// storedRequested is one requested name of a stored day.
type storedRequested struct {
	M string   `json:"m"`
	N counters `json:"n"`
}

func (d *dayRows) storedRequested() []storedRequested {
	if len(d.asked) == 0 {
		return nil
	}
	out := make([]storedRequested, 0, len(d.asked))
	for _, name := range slices.Sorted(maps.Keys(d.asked)) {
		out = append(out, storedRequested{M: name, N: *d.asked[name]})
	}
	return out
}

func requestedOf(stored []storedRequested) map[string]*counters {
	out := make(map[string]*counters, len(stored))
	for _, entry := range stored {
		if entry.M == "" {
			continue
		}
		n := entry.N
		if c, ok := out[entry.M]; ok {
			c.add(n)
			continue
		}
		out[entry.M] = &n
	}
	return out
}

// RequestedModels is the answer's requested_models, given with include=requested_models: requests
// and tokens by the model name the clients sent, over the whole range (filters do not apply).
type RequestedModels struct {
	// From is the first day of the range that holds requested names: days recorded before this
	// version count only under rows. Null when no day of the range has them.
	From      *string          `json:"from"`
	Models    []RequestedModel `json:"models"`
	Truncated bool             `json:"truncated"`
}

// RequestedModel is one requested name.
type RequestedModel struct {
	Model    string `json:"model"`
	Requests int64  `json:"requests"`
	Failures int64  `json:"failures"`
	Tokens   int64  `json:"tokens"`
}

func requestedModelsOf(days []queryDay) *RequestedModels {
	out := &RequestedModels{Models: []RequestedModel{}}
	total := map[string]*counters{}
	for _, day := range days {
		if day.rows == nil || len(day.rows.asked) == 0 {
			continue
		}
		if out.From == nil {
			from := day.day
			out.From = &from
		}
		for name, n := range day.rows.asked {
			if c, ok := total[name]; ok {
				c.add(*n)
			} else {
				copied := *n
				total[name] = &copied
			}
		}
	}
	for name, n := range total {
		out.Models = append(out.Models, RequestedModel{Model: name, Requests: n.Req, Failures: n.Fail, Tokens: n.In + n.Out})
	}
	slices.SortFunc(out.Models, func(a, b RequestedModel) int {
		if a.Requests != b.Requests {
			if a.Requests > b.Requests {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Model, b.Model)
	})
	if len(out.Models) > maxBreakdownRows {
		out.Models, out.Truncated = out.Models[:maxBreakdownRows], true
	}
	return out
}
