package modellists

import (
	"strings"
	"sync/atomic"
	"time"
)

// Provimux: the `figures` object of contract 1 (.github/provimux/design/prices-and-cache/openapi.yaml,
// components/schemas/Figures; prices-and-cache.md 5 and 6.3). One builder for both paths that give it:
// GET /v1/models?include=figures (sdk/api/handlers/openai) and GET /v8/management/provimux/figures
// (internal/api/handlers/management). The two differ only in which entries they select, and each selects from
// the `answered` items of the rows it gives itself: an entry no answer named is never given, and nothing is
// selected by an account's kind, provider or host.

// The values of figures.state.
const (
	FiguresStateHeld = "held"
	FiguresStateNone = "none"
	FiguresStateOff  = "off"
)

// FiguresView is the figures object as both paths write it. Unknown is null, never left out.
type FiguresView struct {
	Contract       int                `json:"contract"`
	Source         string             `json:"source"`
	State          string             `json:"state"`
	DatasetVersion *string            `json:"dataset_version"`
	GeneratedAt    *string            `json:"generated_at"`
	FetchedAt      *string            `json:"fetched_at"`
	Entries        map[string]Figures `json:"entries"`
}

// BuildFiguresView builds the figures object. on is models.published-lists as it is in force (off in Home
// mode): off, the state is off and no figure is given, whatever this process still holds from before the
// switch. doc is the held document, nil when none is held. selected reports whether an entry is one the
// caller's rows name; nil selects none. Only held entries can be given: a selected entry that is not held is
// simply not a key, which is how a tool tells "no figures for this entry" from the two other cases (an answer
// that named no entry, and no answer at all), which it reads on the rows.
func BuildFiguresView(on bool, doc *FiguresDocument, selected func(ref string) bool) FiguresView {
	view := FiguresView{Contract: FiguresContract, Source: FiguresSource, State: FiguresStateOff, Entries: map[string]Figures{}}
	if !on {
		return view
	}
	view.State = FiguresStateNone
	if doc == nil {
		return view
	}
	view.State = FiguresStateHeld
	view.DatasetVersion = figuresViewString(doc.DatasetVersion)
	view.GeneratedAt = figuresViewTime(doc.GeneratedAt)
	view.FetchedAt = figuresViewTime(doc.FetchedAt)
	if selected == nil {
		return view
	}
	for ref, entry := range doc.Entries {
		if selected(ref) {
			view.Entries[ref] = entry
		}
	}
	return view
}

// SelectEntries is a selection over a set of entry references, as both paths collect them from `answered`.
func SelectEntries(refs map[string]bool) func(string) bool {
	return func(ref string) bool { return refs[ref] }
}

// SelectAll selects every held entry (GET /v8/management/provimux/figures?all=true).
func SelectAll(string) bool { return true }

func figuresViewString(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func figuresViewTime(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	value := t.UTC().Format(time.RFC3339Nano)
	return &value
}

// The held document, for the client list. sdk/cliproxy holds the figures and registers this reader at init; the
// management path reads the same store through its own status source (it also shows the failure and the disk
// copy's flag). A binary without sdk/cliproxy holds nothing.
var figuresHeld atomic.Pointer[func() *FiguresDocument]

// SetFiguresHeld registers the reader of the held document; nil removes it.
func SetFiguresHeld(read func() *FiguresDocument) {
	if read == nil {
		figuresHeld.Store(nil)
		return
	}
	figuresHeld.Store(&read)
}

// HeldFigures is the held document, nil when none is held or no reader is registered.
func HeldFigures() *FiguresDocument {
	read := figuresHeld.Load()
	if read == nil || *read == nil {
		return nil
	}
	return (*read)()
}
