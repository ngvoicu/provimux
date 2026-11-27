package modellists

import (
	"fmt"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
)

// The one builder of the figures object (prices-and-cache/openapi.yaml, Figures), shared by
// /v1/models?include=figures and GET /v8/management/provimux/figures.
func TestBuildFiguresView(t *testing.T) {
	// date-safe: the document's dates are written as they are; no clock compares them.
	generated, fetched := time.Date(2026, 11, 26, 11, 22, 48, 384000000, time.UTC), time.Date(2026, 11, 26, 14, 0, 3, 0, time.FixedZone("x", 2*3600))
	doc := &FiguresDocument{Schema: FiguresSchema, DatasetVersion: "c37885999c67cfaa", GeneratedAt: generated, FetchedAt: fetched,
		Entries: map[string]Figures{
			"anthropic/x-1": {Provider: "anthropic", Model: "x-1", Access: "api"},
			"devin/y-2":     {Provider: "devin", Model: "y-2", Access: "subscription"},
		}}
	named := SelectEntries(map[string]bool{"anthropic/x-1": true, "openai/not-held": true})

	write := func(view FiguresView) string {
		out, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	empty := `{"contract":1,"source":"https://providers.dev","state":"%s","dataset_version":null,"generated_at":null,"fetched_at":null,"entries":{}}`
	for name, tc := range map[string]struct {
		view FiguresView
		want string
	}{
		// Off: nothing, whatever is held and selected.
		"off, held":    {BuildFiguresView(false, doc, SelectAll), fmt.Sprintf(empty, "off")},
		"off, nothing": {BuildFiguresView(false, nil, nil), fmt.Sprintf(empty, "off")},
		// None: nothing held.
		"none": {BuildFiguresView(true, nil, SelectAll), fmt.Sprintf(empty, "none")},
		// Held, nothing selected: the dataset's dates, no entry.
		// date-safe: the expected text of the dates above.
		"held, none selected": {BuildFiguresView(true, doc, nil), `{"contract":1,"source":"https://providers.dev","state":"held","dataset_version":"c37885999c67cfaa","generated_at":"2026-11-26T11:22:48.384Z","fetched_at":"2026-11-26T12:00:03Z","entries":{}}`},
	} {
		if got := write(tc.view); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", name, got, tc.want)
		}
	}
	// Held, selected: only the selected entries that are held; one named but not held is no key.
	view := BuildFiguresView(true, doc, named)
	if len(view.Entries) != 1 || view.Entries["anthropic/x-1"].Model != "x-1" {
		t.Fatalf("selected: %+v", view.Entries)
	}
	if _, ok := view.Entries["openai/not-held"]; ok {
		t.Fatal("an entry not held is a key")
	}
	if all := BuildFiguresView(true, doc, SelectAll); len(all.Entries) != 2 {
		t.Fatalf("all: %+v", all.Entries)
	}
	// The builder never changes the held document.
	if len(doc.Entries) != 2 {
		t.Fatal("the document changed")
	}
	// A held document without dates gives null, never a zero time.
	bare := BuildFiguresView(true, &FiguresDocument{Entries: map[string]Figures{}}, nil)
	if bare.DatasetVersion != nil || bare.GeneratedAt != nil || bare.FetchedAt != nil || bare.State != FiguresStateHeld {
		t.Fatalf("bare: %+v", bare)
	}
}

func TestHeldFiguresReader(t *testing.T) {
	SetFiguresHeld(nil)
	t.Cleanup(func() { SetFiguresHeld(nil) })
	if HeldFigures() != nil {
		t.Fatal("no reader: something held")
	}
	doc := &FiguresDocument{Entries: map[string]Figures{}}
	SetFiguresHeld(func() *FiguresDocument { return doc })
	if HeldFigures() != doc {
		t.Fatal("the reader's document is not given")
	}
	SetFiguresHeld(nil)
	if HeldFigures() != nil {
		t.Fatal("a removed reader still gives")
	}
}
