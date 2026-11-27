package cliproxy

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	moejson "github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/regexp"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/modellists/pdentries"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"gopkg.in/yaml.v3"
)

// Step F3 (prices-and-cache.md 5 and 6.3): GET /v1/models?include=figures and GET /v8/management/provimux/figures,
// on the wire, through the real server: real answers from the stand-in vendor make the rows' `answered`, providers.dev's
// stand-in (pdentries, loopback) gives the held figures, and every answer of both paths is checked against
// .github/provimux/design/prices-and-cache/openapi.yaml with unknown properties forbidden. Nothing leaves the process
// but loopback; every key, login, id and invented model is the test's own.

// ---- the contract, strictly ---------------------------------------------------------------------------------------

func figuresContractDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", ".github", "provimux", "design", "prices-and-cache")
}

func figuresContractSchemas(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(figuresContractDir(), "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("openapi.yaml: %v", err)
	}
	return doc["components"].(map[string]any)["schemas"].(map[string]any)
}

// figuresValidate checks document against a schema of the contract. Strict: a property a schema does not list is an
// error wherever the schema has no additionalProperties. It reads what the contract uses: $ref, oneOf, type (with
// null), const, enum, required, properties, additionalProperties, items, maxItems, minimum, pattern, format date-time
// (RFC 3339 in UTC) and dependentRequired.
func figuresValidate(t *testing.T, schemas map[string]any, schema string, document []byte) {
	t.Helper()
	var value any
	if err := moejson.Unmarshal(document, &value); err != nil {
		t.Fatalf("not JSON: %v: %s", err, document)
	}
	if errs := figuresCheck(schemas, schemas[schema], value, "$"); len(errs) > 0 {
		sort.Strings(errs)
		t.Fatalf("against %s:\n%s\n%s", schema, strings.Join(errs, "\n"), document)
	}
}

func figuresCheck(schemas map[string]any, node, value any, path string) []string {
	schema, _ := node.(map[string]any)
	if schema == nil {
		return []string{path + ": no schema"}
	}
	if ref, ok := schema["$ref"].(string); ok {
		return figuresCheck(schemas, schemas[strings.TrimPrefix(ref, "#/components/schemas/")], value, path)
	}
	if options, ok := schema["oneOf"].([]any); ok {
		matched := 0
		for _, option := range options {
			if len(figuresCheck(schemas, option, value, path)) == 0 {
				matched++
			}
		}
		if matched != 1 {
			return []string{fmt.Sprintf("%s: %d oneOf branches match %v", path, matched, value)}
		}
		return nil
	}
	var types []string
	switch declared := schema["type"].(type) {
	case string:
		types = []string{declared}
	case []any:
		for _, item := range declared {
			types = append(types, fmt.Sprint(item))
		}
	}
	if len(types) > 0 && !figuresTypeMatches(types, value) {
		return []string{fmt.Sprintf("%s: %T %v is not %v", path, value, value, types)}
	}
	if want, ok := schema["const"]; ok && fmt.Sprint(want) != fmt.Sprint(value) {
		return []string{fmt.Sprintf("%s: %v is not the constant %v", path, value, want)}
	}
	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, item := range enum {
			found = found || fmt.Sprint(item) == fmt.Sprint(value)
		}
		if !found {
			return []string{fmt.Sprintf("%s: %v not in %v", path, value, enum)}
		}
	}
	var errs []string
	switch typed := value.(type) {
	case string:
		if pattern, ok := schema["pattern"].(string); ok && !regexp.MustCompile(pattern).MatchString(typed) {
			errs = append(errs, fmt.Sprintf("%s: %q does not match %s", path, typed, pattern))
		}
		if schema["format"] == "date-time" {
			parsed, err := time.Parse(time.RFC3339Nano, typed)
			if err != nil || !strings.HasSuffix(typed, "Z") || parsed.Location() != time.UTC {
				errs = append(errs, fmt.Sprintf("%s: %q is not an RFC 3339 time in UTC", path, typed))
			}
		}
	case float64:
		if minimum, ok := schema["minimum"].(int); ok && typed < float64(minimum) {
			errs = append(errs, fmt.Sprintf("%s: %v below %d", path, typed, minimum))
		}
	case map[string]any:
		required, _ := schema["required"].([]any)
		for _, key := range required {
			if _, ok := typed[fmt.Sprint(key)]; !ok {
				errs = append(errs, fmt.Sprintf("%s: missing %v", path, key))
			}
		}
		if dependent, ok := schema["dependentRequired"].(map[string]any); ok {
			for key, needs := range dependent {
				if _, present := typed[key]; !present {
					continue
				}
				for _, need := range needs.([]any) {
					if _, ok := typed[fmt.Sprint(need)]; !ok {
						errs = append(errs, fmt.Sprintf("%s: %s without %v", path, key, need))
					}
				}
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		additional, hasAdditional := schema["additionalProperties"]
		for key, item := range typed {
			property, listed := properties[key]
			switch {
			case listed:
				errs = append(errs, figuresCheck(schemas, property, item, path+"."+key)...)
			case hasAdditional:
				errs = append(errs, figuresCheck(schemas, additional, item, path+"["+key+"]")...)
			default:
				errs = append(errs, fmt.Sprintf("%s: %s is not in the contract", path, key))
			}
		}
	case []any:
		if maximum, ok := schema["maxItems"].(int); ok && len(typed) > maximum {
			errs = append(errs, fmt.Sprintf("%s: %d items, at most %d", path, len(typed), maximum))
		}
		for i, item := range typed {
			errs = append(errs, figuresCheck(schemas, schema["items"], item, fmt.Sprintf("%s[%d]", path, i))...)
		}
	}
	return errs
}

func figuresTypeMatches(types []string, value any) bool {
	for _, want := range types {
		switch typed := value.(type) {
		case nil:
			if want == "null" {
				return true
			}
		case string:
			if want == "string" {
				return true
			}
		case bool:
			if want == "boolean" {
				return true
			}
		case float64:
			if want == "number" || (want == "integer" && typed == float64(int64(typed))) {
				return true
			}
		case map[string]any:
			if want == "object" {
				return true
			}
		case []any:
			if want == "array" {
				return true
			}
		}
	}
	return false
}

// The validator itself: the contract's own examples pass, and each kind of fault it is relied on for fails.
func TestProvimuxFiguresContractValidator(t *testing.T) {
	schemas := figuresContractSchemas(t)
	for name, schema := range map[string]string{"models-figures.json": "ModelList", "figures.json": "FiguresStatus"} {
		data, err := os.ReadFile(filepath.Join(figuresContractDir(), "examples", name))
		if err != nil {
			t.Fatal(err)
		}
		figuresValidate(t, schemas, schema, data)
	}
	// date-safe: an invented day and no time; the validator compares neither with a clock.
	good := `{"object":"list","data":[{"id":"m","object":"model","accounts":[{"provider":"claude","kind":"apikey","priority":0,"account":"a.aaaaaaaaaaaaaaaaaaaa","answered":[{"entry":null,"on":"2026-01-02"}]}]}],` +
		`"figures":{"contract":1,"source":"https://providers.dev","state":"none","dataset_version":null,"generated_at":null,"fetched_at":null,"entries":{}}}`
	var value any
	if err := moejson.Unmarshal([]byte(good), &value); err != nil {
		t.Fatal(err)
	}
	if errs := figuresCheck(schemas, schemas["ModelList"], value, "$"); len(errs) != 0 {
		t.Fatalf("a good document fails: %v", errs)
	}
	// date-safe: the faults below edit the invented day of the document above; nothing compares them with a clock.
	item := `{"entry":null,"on":"2026-01-02"}`
	for name, bad := range map[string]string{
		"an unknown property":      strings.Replace(good, `"object":"list"`, `"object":"list","extra":1`, 1),
		"an unknown row property":  strings.Replace(good, `"priority":0`, `"priority":0,"entry":"anthropic/x"`, 1),
		"an entry not a record":    strings.Replace(good, `"entries":{}`, `"entries":{"anthropic/x":{"provider":"anthropic"}}`, 1),
		"a wrong contract":         strings.Replace(good, `"contract":1`, `"contract":2`, 1),
		"a wrong state":            strings.Replace(good, `"state":"none"`, `"state":"stale"`, 1),
		"a marker out of pattern":  strings.Replace(good, `a.aaaaaaaaaaaaaaaaaaaa`, `x.1`, 1),
		"a day out of pattern":     strings.Replace(good, `"on":"2026-01-02"`, `"on":"2026-01-02T00:00:00Z"`, 1),
		"a missing required key":   strings.Replace(good, `"dataset_version":null,`, ``, 1),
		"a time not in UTC":        strings.Replace(good, `"fetched_at":null`, `"fetched_at":"2026-01-02T03:04:05+02:00"`, 1),
		"an item without its day":  strings.Replace(good, `,"on":"2026-01-02"`, ``, 1),
		"five answered items":      strings.Replace(good, "["+item+"]", "["+strings.Repeat(item+",", 4)+item+"]", 1),
		"an entry ref out of form": strings.Replace(good, `"entry":null`, `"entry":"no-slash"`, 1),
	} {
		if bad == good {
			t.Fatalf("%s: the fixture did not change", name)
		}
		if err := moejson.Unmarshal([]byte(bad), &value); err != nil {
			t.Fatal(err)
		}
		if errs := figuresCheck(schemas, schemas["ModelList"], value, "$"); len(errs) == 0 {
			t.Errorf("%s passes", name)
		}
	}
}

// ---- the gateway ---------------------------------------------------------------------------------------------------

const (
	figuresNative    = "provimux-test-native"
	figuresAlternate = "provimux-test-alternate"
)

// figuresContractConfig: a Claude key with an alias (its answers' entry is held), a model of its own without an alias
// (held; not one of "my names"), an alias never asked for; a house key (its answers name no entry); a rule by header
// that sends one Copilot name as two models (one held, one not).
const figuresContractConfig = `api-keys:
  claude:
    - name: provimux-test-claude
      models:
        - name: provimux-test-claude-sent
          alias: provimux-test-claude-key
        - name: provimux-test-claude-other
        - name: provimux-test-claude-idle-sent
          alias: provimux-test-claude-idle
      keys:
        - api-key: sk-provimux-test-claude-key
  openai-compatibility:
    - name: provimux-test-house
      base-url: https://llm.provimux-test.example/v1
      models:
        - name: provimux-test-house-sent
          alias: provimux-test-house
      keys:
        - api-key: sk-provimux-test-house
payload:
  override:
    - models:
        - name: ` + figuresNative + `
          headers:
            X-Client-Tier: special
      params:
        model: ` + figuresAlternate + `
models:
  answered-by: true
  published-lists: true
`

// The entries providers.dev's stand-in has: the twelve real summaries of testdata, and three made from the first for
// the invented models. github-copilot/provimux-test-alternate is not among them.
var figuresContractMade = []string{"anthropic/provimux-test-claude-sent", "anthropic/provimux-test-claude-other", "github-copilot/" + figuresNative}

type figuresGateway struct {
	*sentHarness
	t       *testing.T
	schemas map[string]any
	pd      *pdentries.Server
	clock   *sentClock
}

func newFiguresGateway(t *testing.T) *figuresGateway {
	t.Helper()
	h := newSentHarness(t, figuresContractConfig)
	clock := newSentClock()
	useAnsweredBook(t, clock)
	provimuxFiguresReset(t)
	t.Cleanup(func() { registry.UpdateModelCatalogSources(registry.CatalogSources{}, false) })
	version, generated, entries := provimuxFiguresEntries(t, 0)
	for _, ref := range figuresContractMade {
		provider, model, _ := strings.Cut(ref, "/")
		entries = append(entries, pdentries.Edit(entries[0], func(entry map[string]any) {
			entry["provider"], entry["model"], entry["ref"] = provider, model, ref
		}))
	}
	pd := pdentries.New(entries, version, generated)
	t.Cleanup(pd.Close)
	provimuxFiguresBase = pd.EntriesURL()
	g := &figuresGateway{sentHarness: h, t: t, schemas: figuresContractSchemas(t), pd: pd, clock: clock}
	g.apply(func(*config.ModelCatalogs) {}) // the config's policy in force, as a start puts it
	return g
}

// apply commits the current config with edit applied, as a reload does: the client list's switch, the management
// handler's config, the answered-by switch and the names rule.
func (g *figuresGateway) apply(edit func(*config.ModelCatalogs)) {
	g.t.Helper()
	next := g.service.currentConfig().CloneForRuntime()
	edit(&next.Models)
	g.service.applyWatcherConfigUpdate(next)
}

// hold runs the figures' part of the round once: the stand-in's dataset becomes the held document.
func (g *figuresGateway) hold() {
	g.t.Helper()
	g.service.provimuxFiguresRound(g.t.Context(), g.service.currentConfig())
	if doc := provimuxFiguresHeld(); doc == nil || len(doc.Entries) != 12+len(figuresContractMade) {
		g.t.Fatalf("fixture: held %+v, requests %d", doc, len(g.pd.Requests()))
	}
}

func (g *figuresGateway) get(path, key string) []byte {
	g.t.Helper()
	request, _ := http.NewRequest(http.MethodGet, g.server.URL+path, nil)
	request.Header.Set("Authorization", "Bearer "+key)
	response, err := g.server.Client().Do(request)
	if err != nil {
		g.t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		g.t.Fatalf("GET %s: %d %s", path, response.StatusCode, body)
	}
	return body
}

type figuresListed struct {
	Data []struct {
		ID       string           `json:"id"`
		Accounts []listedAnswered `json:"accounts"`
	} `json:"data"`
	Figures *struct {
		State   string                        `json:"state"`
		Entries map[string]moejson.RawMessage `json:"entries"`
	} `json:"figures"`
}

// clientList reads /v1/models?include=accounts,figures, checks it against the contract, checks that every other
// spelling of the request and the other client key give the same bytes and that include=accounts is the same list
// without figures, and returns it with its body.
func (g *figuresGateway) clientList(example string) (figuresListed, []byte) {
	g.t.Helper()
	body := g.get("/v1/models?include=accounts,figures", sentClientKey)
	figuresValidate(g.t, g.schemas, "ModelList", body)
	for _, query := range []string{"include=figures", "include=FIGURES", "include=accounts&include=figures", "include=figures,unknown", "include=%20figures%20,accounts"} {
		if other := g.get("/v1/models?"+query, sentClientKey); string(other) != string(body) {
			g.t.Fatalf("?%s differs from include=accounts,figures:\n%s\n%s", query, other, body)
		}
	}
	g.otherCaller(body)
	var withFigures, accounts map[string]any
	if err := moejson.Unmarshal(body, &withFigures); err != nil {
		g.t.Fatal(err)
	}
	if err := moejson.Unmarshal(g.get("/v1/models?include=accounts", sentClientKey), &accounts); err != nil {
		g.t.Fatal(err)
	}
	if _, ok := accounts["figures"]; ok {
		g.t.Fatal("include=accounts gives figures")
	}
	delete(withFigures, "figures")
	a, _ := moejson.Marshal(withFigures)
	b, _ := moejson.Marshal(accounts)
	if string(a) != string(b) {
		g.t.Fatalf("include=figures's rows differ from include=accounts's:\n%s\n%s", a, b)
	}
	var listed figuresListed
	if err := moejson.Unmarshal(body, &listed); err != nil {
		g.t.Fatal(err)
	}
	if listed.Figures == nil {
		g.t.Fatalf("no figures: %s", body)
	}
	figuresWriteExample(g.t, "models-"+example+".json", body)
	return listed, body
}

// figuresNamed is the set of entries the `answered` items of a list's rows name.
func figuresNamed(listed figuresListed) map[string]bool {
	named := map[string]bool{}
	for _, model := range listed.Data {
		for i := range model.Accounts {
			for _, entry := range figuresItems(&model.Accounts[i]) {
				if entry != "null" {
					named[entry] = true
				}
			}
		}
	}
	return named
}

// figuresWithoutAnswers is a list with every row's `answered` and the figures' entries left out: what two callers
// share.
func figuresWithoutAnswers(t *testing.T, body []byte) string {
	t.Helper()
	var value map[string]any
	if err := moejson.Unmarshal(body, &value); err != nil {
		t.Fatalf("not JSON: %s", body)
	}
	for _, item := range value["data"].([]any) {
		for _, row := range item.(map[string]any)["accounts"].([]any) {
			delete(row.(map[string]any), "answered")
		}
	}
	if figures, ok := value["figures"].(map[string]any); ok {
		delete(figures, "entries")
	}
	out, _ := moejson.Marshal(value)
	return string(out)
}

// otherCaller checks the list the other client key reads beside body (the caller's): `answered` is kept per caller,
// so the other key gets the same models and rows, its own `answered`, and the figures of the entries its own rows name
// only, never one only the caller's answers named.
func (g *figuresGateway) otherCaller(body []byte) {
	g.t.Helper()
	other := g.get("/v1/models?include=accounts,figures", sentOtherClientKey)
	figuresValidate(g.t, g.schemas, "ModelList", other)
	if a, b := figuresWithoutAnswers(g.t, other), figuresWithoutAnswers(g.t, body); a != b {
		g.t.Fatalf("another client key gets other models or rows:\n%s\n%s", a, b)
	}
	var mine, theirs figuresListed
	if err := moejson.Unmarshal(body, &mine); err != nil {
		g.t.Fatal(err)
	}
	if err := moejson.Unmarshal(other, &theirs); err != nil {
		g.t.Fatal(err)
	}
	named := figuresNamed(theirs)
	for ref := range theirs.Figures.Entries {
		if !named[ref] {
			g.t.Fatalf("another client key is given %s, which none of its own answers named:\n%s", ref, other)
		}
	}
	for ref, record := range mine.Figures.Entries {
		if mine.Figures.State != theirs.Figures.State || (named[ref] && string(theirs.Figures.Entries[ref]) != string(record)) {
			g.t.Fatalf("the two keys read %s differently:\n%s\n%s", ref, other, body)
		}
	}
}

// status reads GET /v8/management/provimux/figures (query "" or "?all=true") and checks it against the contract.
func (g *figuresGateway) status(query, example string) provimuxFiguresAnswer {
	g.t.Helper()
	body := g.get("/v8/management/provimux/figures"+query, namesManagementKey)
	figuresValidate(g.t, g.schemas, "FiguresStatus", body)
	var answer provimuxFiguresAnswer
	if err := moejson.Unmarshal(body, &answer); err != nil {
		g.t.Fatal(err)
	}
	if example != "" {
		figuresWriteExample(g.t, "status-"+example+".json", body)
	}
	return answer
}

// figuresWriteExample keeps an answer as an example where PMX_FIGURES_EXAMPLES names a directory (for the task's
// result; nothing reads it back).
func figuresWriteExample(t *testing.T, name string, body []byte) {
	dir := os.Getenv("PMX_FIGURES_EXAMPLES")
	if dir == "" {
		return
	}
	var value any
	_ = moejson.Unmarshal(body, &value)
	pretty, _ := moejson.MarshalIndent(value, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, name), append(pretty, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func figuresKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// row is the client list's row of the account with marker under model, or nil.
func (l figuresListed) row(model, marker string) *listedAnswered {
	for _, m := range l.Data {
		for i := range m.Accounts {
			if m.ID == model && m.Accounts[i].Account == marker {
				return &m.Accounts[i]
			}
		}
	}
	return nil
}

func (l figuresListed) listed(model string) bool {
	for _, m := range l.Data {
		if m.ID == model {
			return true
		}
	}
	return false
}

// wantEntries checks the state and the entries of both paths (management without ?all=true), and that the two give
// one entry the same record byte for byte.
func (g *figuresGateway) wantEntries(what string, listed figuresListed, state string, want ...string) {
	g.t.Helper()
	want = append([]string{}, want...)
	sort.Strings(want)
	if listed.Figures.State != state || strings.Join(figuresKeys(listed.Figures.Entries), ",") != strings.Join(want, ",") {
		g.t.Fatalf("%s: /v1/models figures %s %v, want %s %v", what, listed.Figures.State, figuresKeys(listed.Figures.Entries), state, want)
	}
	status := g.status("", "")
	if status.Figures.State != state || strings.Join(figuresKeys(status.Figures.Entries), ",") != strings.Join(want, ",") {
		g.t.Fatalf("%s: management figures %s %v, want %s %v", what, status.Figures.State, figuresKeys(status.Figures.Entries), state, want)
	}
	for _, ref := range want {
		if string(listed.Figures.Entries[ref]) != string(status.Figures.Entries[ref]) {
			g.t.Fatalf("%s: %s differs between the paths:\n%s\n%s", what, ref, listed.Figures.Entries[ref], status.Figures.Entries[ref])
		}
	}
}

// figuresItems is the row's answered as entries ("null" for none), newest first; nil without answered.
func figuresItems(row *listedAnswered) []string {
	if row == nil || row.Answered == nil {
		return nil
	}
	out := []string{}
	for _, item := range *row.Answered {
		entry := "null"
		if item.Entry != nil {
			entry = *item.Entry
		}
		out = append(out, entry)
	}
	return out
}

// 1 to 5. Both paths, in each state: off, none held, held; no answer yet, one entry, two entries (one held, one not),
// an answer without an entry; under "serve only my names"; with the statement off. Every answer follows the contract
// with unknown properties forbidden, the selection is exactly the held entries the listed rows' answered name, and the
// three cases without figures stay apart.
func TestProvimuxFiguresBothPathsFollowTheContract(t *testing.T) {
	g := newFiguresGateway(t)
	key := sentKeyFor(t, g.sentHarness, "provimux-test-claude-key")
	house := sentKeyFor(t, g.sentHarness, "provimux-test-house")
	// A login as the gateway's own loader makes one (the entry branch's helper): an SDK registration in memory names
	// no entry (the amendment of 2026-11-27 (2), rule A.3).
	copilot := sentCopilot(t, g.sentHarness, figuresNative, figuresAlternate)
	route := "copilot/" + figuresNative
	plain := g.get("/v1/models", sentClientKey)

	// No answer yet, nothing held: every row's answered is empty, state none, no entry; the status the same.
	listed, _ := g.clientList("none-no-answer")
	if items := figuresItems(listed.row("provimux-test-claude-key", key.ProvimuxAccountMarker())); items == nil || len(items) != 0 {
		t.Fatalf("no answer yet: %v", items)
	}
	g.wantEntries("none, no answer", listed, "none")
	if status := g.status("?all=true", "none"); status.HeldEntries != 0 || !status.Options.PublishedLists {
		t.Fatalf("none: %+v", status)
	}

	// Answers: an alias (its entry held), the key's own id (held), the house key (no entry), Copilot's two models.
	g.clock.add(time.Hour)
	g.checkSent(g.post("/v1/chat/completions", chatBody("provimux-test-claude-key", false, ""), nil), key, "api.anthropic.com", "anthropic/provimux-test-claude-sent")
	g.stand.reset()
	g.checkSent(g.post("/v1/chat/completions", chatBody("provimux-test-claude-other", false, ""), nil), key, "api.anthropic.com", "anthropic/provimux-test-claude-other")
	g.stand.reset()
	g.checkSent(g.post("/v1/chat/completions", chatBody("provimux-test-house", false, ""), nil), house, "llm.provimux-test.example", "")
	for _, tier := range []string{"normal", "special"} {
		g.clock.add(time.Minute)
		g.stand.reset()
		if answer := g.post("/v1/chat/completions", chatBody(route, false, ""), sentHeader("X-Client-Tier", tier)); answer.status != http.StatusOK {
			t.Fatalf("fixture: Copilot %s: %d %s", tier, answer.status, answer.body)
		}
	}

	// Nothing held yet: the rows say what was answered, and no figure is given.
	listed, _ = g.clientList("none-answered")
	g.wantEntries("none, answered", listed, "none")

	// Held: exactly the held entries the rows name.
	g.hold()
	listed, _ = g.clientList("held")
	if got := figuresItems(listed.row(route, copilot.ProvimuxAccountMarker())); strings.Join(got, ",") != "github-copilot/"+figuresAlternate+",github-copilot/"+figuresNative {
		t.Fatalf("two entries: %v", got)
	}
	g.wantEntries("held", listed, "held", figuresContractMade...)
	// The three cases without figures, each told apart:
	// 1. the entry is not held: an item names it, and it is no key of entries;
	if _, ok := listed.Figures.Entries["github-copilot/"+figuresAlternate]; ok {
		t.Fatal("an entry not held is given")
	}
	// 2. the answers named no entry: an item whose entry is null;
	if got := figuresItems(listed.row("provimux-test-house", house.ProvimuxAccountMarker())); strings.Join(got, ",") != "null" {
		t.Fatalf("an answer without an entry: %v", got)
	}
	// 3. no answer yet: answered is empty. Nothing is filled in from the account: the idle alias's account has held
	// entries under its other names, and anthropic/claude-opus-5-5 is held for its kind and provider; neither is given
	// for it.
	if got := figuresItems(listed.row("provimux-test-claude-idle", key.ProvimuxAccountMarker())); got == nil || len(got) != 0 {
		t.Fatalf("no answer yet: %v", got)
	}
	if _, ok := listed.Figures.Entries["anthropic/claude-opus-5-5"]; ok {
		t.Fatal("an entry no answer named is given")
	}
	all := g.status("?all=true", "held-all")
	if all.HeldEntries != 12+len(figuresContractMade) || len(all.Figures.Entries) != all.HeldEntries {
		t.Fatalf("?all=true: %d held, %d given", all.HeldEntries, len(all.Figures.Entries))
	}
	g.status("", "held")
	if got := g.get("/v1/models", sentClientKey); string(got) != string(plain) {
		t.Fatalf("the list without include changed:\n%s\n%s", got, plain)
	}

	// Serve only my names: the key's own id is not served, so its row is not listed and its held entry is not given,
	// on either path, though its answer is recorded.
	g.apply(func(m *config.ModelCatalogs) {
		m.OnlyMyNames = &registry.OnlyMyNames{Enabled: true, Keep: []string{route}}
	})
	listed, _ = g.clientList("held-only-my-names")
	if listed.listed("provimux-test-claude-other") || !listed.listed("provimux-test-claude-key") || !listed.listed(route) {
		t.Fatalf("fixture: the names served under the rule: %+v", listed.Data)
	}
	g.wantEntries("only my names", listed, "held", "anthropic/provimux-test-claude-sent", "github-copilot/"+figuresNative)
	g.apply(func(m *config.ModelCatalogs) { m.OnlyMyNames = nil })

	// Off (models.published-lists: false): state off and no entry on both paths, whatever is held; the rows keep what
	// was answered.
	g.apply(func(m *config.ModelCatalogs) { m.PublishedLists = new(false) })
	listed, _ = g.clientList("off")
	g.wantEntries("off", listed, "off")
	if got := figuresItems(listed.row("provimux-test-claude-key", key.ProvimuxAccountMarker())); len(got) != 1 {
		t.Fatalf("off: the row lost its record: %v", got)
	}
	if status := g.status("?all=true", "off"); status.HeldEntries != 0 || len(status.Figures.Entries) != 0 || status.Options.PublishedLists {
		t.Fatalf("off, all: %+v", status)
	}
	if got := g.get("/v1/models", sentClientKey); figuresCanonical(t, got) != figuresCanonical(t, plain) || strings.Contains(string(got), `"figures"`) {
		t.Fatalf("the list without include changed with the switch:\n%s\n%s", got, plain)
	}

	// The statement off (models.answered-by: false): no row has account or answered, so nothing leads to a figure.
	g.apply(func(m *config.ModelCatalogs) { m.PublishedLists, m.AnsweredBy = new(true), new(false) })
	listed, body := g.clientList("held-answered-by-off")
	if strings.Contains(string(body), `"answered"`) || strings.Contains(string(body), `"account"`) {
		t.Fatalf("answered-by off: %s", body)
	}
	g.wantEntries("answered-by off", listed, "held")
}

// figuresCanonical is a list with its models in the order of their ids and without `created`, for a comparison
// across a reload. A reload registers the keys again: upstream's registry may then give the same models in another
// order, and stamps them with the second of the new registration. Comparisons without a reload in between compare
// the bytes as written.
func figuresCanonical(t *testing.T, body []byte) string {
	t.Helper()
	var value map[string]any
	if err := moejson.Unmarshal(body, &value); err != nil {
		t.Fatalf("not JSON: %s", body)
	}
	for _, key := range []string{"data", "models"} {
		if list, ok := value[key].([]any); ok {
			for _, item := range list {
				if model, ok := item.(map[string]any); ok {
					delete(model, "created")
				}
			}
			sort.SliceStable(list, func(i, j int) bool {
				a, _ := list[i].(map[string]any)
				b, _ := list[j].(map[string]any)
				return fmt.Sprint(a["id"], a["slug"]) < fmt.Sprint(b["id"], b["slug"])
			})
		}
	}
	out, err := moejson.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// 1. A caller who does not ask gets the list as before, byte for byte: include absent, an unknown value, a near miss
// and the Codex-format list are the same bytes once figures are held and an answer is recorded as before (no reload
// in between). Across a reload of the switch, they and include=accounts are the same (figuresCanonical).
func TestProvimuxFiguresNothingForWhoDoesNotAsk(t *testing.T) {
	g := newFiguresGateway(t)
	queries := []string{"", "?include=unknown", "?include=figure", "?client_version=0.99.0"}
	before := map[string][]byte{}
	for _, query := range queries {
		before[query] = g.get("/v1/models"+query, sentClientKey)
	}
	g.hold()
	key := sentKeyFor(t, g.sentHarness, "provimux-test-claude-key")
	g.checkSent(g.post("/v1/chat/completions", chatBody("provimux-test-claude-key", false, ""), nil), key, "api.anthropic.com", "anthropic/provimux-test-claude-sent")
	listed, _ := g.clientList("held-one")
	g.wantEntries("one answer", listed, "held", "anthropic/provimux-test-claude-sent")
	held := map[string][]byte{}
	for _, query := range queries {
		held[query] = g.get("/v1/models"+query, sentClientKey)
		if string(held[query]) != string(before[query]) || strings.Contains(string(held[query]), `"figures"`) {
			t.Fatalf("/v1/models%s changed:\n%s\n%s", query, held[query], before[query])
		}
	}
	on := g.get("/v1/models?include=accounts", sentClientKey)
	g.apply(func(m *config.ModelCatalogs) { m.PublishedLists = new(false) })
	for _, query := range queries {
		if got := g.get("/v1/models"+query, sentClientKey); figuresCanonical(t, got) != figuresCanonical(t, held[query]) {
			t.Fatalf("/v1/models%s changed with the switch:\n%s\n%s", query, got, held[query])
		}
	}
	if off := g.get("/v1/models?include=accounts", sentClientKey); figuresCanonical(t, on) != figuresCanonical(t, off) || strings.Contains(string(on), `"figures"`) {
		t.Fatalf("include=accounts depends on the figures:\n%s\n%s", on, off)
	}
}

// `answered` is kept per caller, and so are the figures: two client keys, each with answers of its own on the same
// account, read /v1/models?include=accounts,figures (and include=figures alone) end to end. Each gets the figures of
// the entries its own rows name and not the other's; the operator's path, which reads every caller's, gets both.
func TestProvimuxFiguresPerCaller(t *testing.T) {
	g := newFiguresGateway(t)
	key := sentKeyFor(t, g.sentHarness, "provimux-test-claude-key")
	g.hold()
	g.clock.add(time.Hour)
	g.checkSent(g.post("/v1/chat/completions", chatBody("provimux-test-claude-key", false, ""), nil), key, "api.anthropic.com", "anthropic/provimux-test-claude-sent")
	g.stand.reset()
	other := sentHeader("Authorization", "Bearer "+sentOtherClientKey)
	g.checkSent(g.post("/v1/chat/completions", chatBody("provimux-test-claude-other", false, ""), other), key, "api.anthropic.com", "anthropic/provimux-test-claude-other")
	marker := key.ProvimuxAccountMarker()
	for _, caller := range []struct {
		key, answered, notAnswered, entry, notEntry string
	}{
		{sentClientKey, "provimux-test-claude-key", "provimux-test-claude-other", "anthropic/provimux-test-claude-sent", "anthropic/provimux-test-claude-other"},
		{sentOtherClientKey, "provimux-test-claude-other", "provimux-test-claude-key", "anthropic/provimux-test-claude-other", "anthropic/provimux-test-claude-sent"},
	} {
		for _, query := range []string{"include=accounts,figures", "include=figures"} {
			body := g.get("/v1/models?"+query, caller.key)
			figuresValidate(t, g.schemas, "ModelList", body)
			var listed figuresListed
			if err := moejson.Unmarshal(body, &listed); err != nil {
				t.Fatal(err)
			}
			if got := figuresItems(listed.row(caller.answered, marker)); strings.Join(got, ",") != caller.entry {
				t.Fatalf("%s ?%s: its own row %v, want %s", caller.key, query, got, caller.entry)
			}
			if got := figuresItems(listed.row(caller.notAnswered, marker)); got == nil || len(got) != 0 {
				t.Fatalf("%s ?%s: the other caller's row %v, want empty", caller.key, query, got)
			}
			if listed.Figures.State != "held" || strings.Join(figuresKeys(listed.Figures.Entries), ",") != caller.entry {
				t.Fatalf("%s ?%s: figures %s %v, want held [%s]", caller.key, query, listed.Figures.State, figuresKeys(listed.Figures.Entries), caller.entry)
			}
			if _, ok := listed.Figures.Entries[caller.notEntry]; ok {
				t.Fatalf("%s ?%s: given %s, which only the other caller's answers named", caller.key, query, caller.notEntry)
			}
		}
	}
	status := g.status("", "")
	if got := strings.Join(figuresKeys(status.Figures.Entries), ","); got != "anthropic/provimux-test-claude-other,anthropic/provimux-test-claude-sent" {
		t.Fatalf("the operator's path: %s, want both callers' entries", got)
	}
}
