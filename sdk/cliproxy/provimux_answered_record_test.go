package cliproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: what each account last answered as, on both lists (answered-by.md 4.4 and 8.4, step W6b). Every key,
// login and id is invented; every time is on a clock the test sets.

// sentClock is a clock a test moves.
type sentClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *sentClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *sentClock) add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// newSentClock starts at the next noon in UTC from now: a day of its own, so no test meets today's date and a few
// hours either way stay on one day.
func newSentClock() *sentClock {
	noon := time.Now().UTC().Truncate(24 * time.Hour).Add(36 * time.Hour)
	return &sentClock{now: noon}
}

// useAnsweredBook puts a record of the test's own in the gateway's place, on clock and store, for the test.
func useAnsweredBook(t *testing.T, clock *sentClock, store func() (provimuxAnsweredStore, error)) *provimuxAnsweredBook {
	t.Helper()
	book := newProvimuxAnsweredBook(clock.Now, store)
	was := provimuxAnswered
	provimuxAnswered = book
	t.Cleanup(func() { provimuxAnswered = was })
	return book
}

// noAnsweredStore is a gateway without a local store.
func noAnsweredStore() (provimuxAnsweredStore, error) {
	return nil, fmt.Errorf("provimux-test: no store")
}

// boltAnsweredStore opens a local store at path for the test.
func boltAnsweredStore(t *testing.T, path string) func() (provimuxAnsweredStore, error) {
	t.Helper()
	store, err := kvstore.OpenBoltStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return func() (provimuxAnsweredStore, error) { return store, nil }
}

// listedAnswered is an account row of either list, as a client reads it.
type listedAnswered struct {
	Account  string `json:"account"`
	Answered *[]struct {
		Entry *string `json:"entry"`
		On    string  `json:"on"`
		At    string  `json:"at"`
	} `json:"answered"`
}

func (r listedAnswered) items() []string {
	if r.Answered == nil {
		return nil
	}
	out := []string{}
	for _, item := range *r.Answered {
		entry := "null"
		if item.Entry != nil {
			entry = *item.Entry
		}
		out = append(out, entry+"@"+item.On+item.At)
	}
	return out
}

// listsRow reads the row of marker under model in /v1/models?include=accounts (with clientKey) and in model-routes.
func (h *sentHarness) listsRow(clientKey, model, marker string) (client, management *listedAnswered, raw string) {
	h.t.Helper()
	get := func(path, key string) []byte {
		request, _ := http.NewRequest(http.MethodGet, h.server.URL+path, nil)
		request.Header.Set("Authorization", "Bearer "+key)
		response, err := h.server.Client().Do(request)
		if err != nil {
			h.t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != http.StatusOK {
			h.t.Fatalf("GET %s: %d %s", path, response.StatusCode, body)
		}
		return body
	}
	var models struct {
		Data []struct {
			ID       string           `json:"id"`
			Accounts []listedAnswered `json:"accounts"`
		} `json:"data"`
	}
	body := get("/v1/models?include=accounts", clientKey)
	if err := json.Unmarshal(body, &models); err != nil {
		h.t.Fatal(err)
	}
	for _, m := range models.Data {
		for i := range m.Accounts {
			if m.ID == model && m.Accounts[i].Account == marker {
				client = &m.Accounts[i]
			}
		}
	}
	var routes struct {
		Models []struct {
			ID     string           `json:"id"`
			Routes []listedAnswered `json:"routes"`
		} `json:"models"`
	}
	if err := json.Unmarshal(get("/v8/management/provimux/model-routes", namesManagementKey), &routes); err != nil {
		h.t.Fatal(err)
	}
	for _, m := range routes.Models {
		for i := range m.Routes {
			if m.ID == model && m.Routes[i].Account == marker {
				management = &m.Routes[i]
			}
		}
	}
	return client, management, string(body)
}

// wantAnswered checks both lists' row of marker under model: the client list with days, the management list with
// times, the same entries in the same order. Items are "entry@" (null for none); the day and time are the clock's.
func (h *sentHarness) wantAnswered(clientKey, model, marker string, at []time.Time, entries ...string) {
	h.t.Helper()
	client, management, raw := h.listsRow(clientKey, model, marker)
	if client == nil || management == nil {
		h.t.Fatalf("no row of %s under %s (client %v, management %v): %s", marker, model, client, management, raw)
	}
	var wantClient, wantManagement []string
	for i, entry := range entries {
		// date-safe: Go's layouts, not dates; the times are the test's clock's.
		wantClient = append(wantClient, entry+"@"+at[i].UTC().Format("2006-01-02"))
		wantManagement = append(wantManagement, entry+"@"+at[i].UTC().Format("2006-01-02T15:04:05.000Z07:00"))
	}
	if got := client.items(); strings.Join(got, ",") != strings.Join(wantClient, ",") || client.Answered == nil {
		h.t.Fatalf("/v1/models row %s %s: answered %v, want %v", model, marker, got, wantClient)
	}
	if got := management.items(); strings.Join(got, ",") != strings.Join(wantManagement, ",") || management.Answered == nil {
		h.t.Fatalf("model-routes row %s %s: answered %v, want %v", model, marker, got, wantManagement)
	}
	for _, item := range *client.Answered {
		if item.At != "" {
			h.t.Fatalf("the client list gives a time: %+v", item)
		}
	}
}

const answeredClaudeConfig = `api-keys:
  claude:
    - name: provimux-test-claude
      models:
        - name: provimux-test-claude-sent
          alias: provimux-test-claude-key
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
`

// 1. A first answer makes the row's first item, on both lists, by the time its first byte is read; before the gateway
// states the answer (read from inside the stand-in) the row has none. An answer with no entry is an item of its own,
// null. A name asked with a thinking suffix lands on the row whose id the account registers.
func TestProvimuxAnsweredFirstAnswerMakesTheRow(t *testing.T) {
	h := newSentHarness(t, answeredClaudeConfig)
	clock := newSentClock()
	useAnsweredBook(t, clock, noAnsweredStore)
	key := sentKeyFor(t, h, "provimux-test-claude-key")
	marker := key.ProvimuxAccountMarker()

	h.wantAnswered(sentClientKey, "provimux-test-claude-key", marker, nil)
	var inside []string
	h.stand.status = func(r *http.Request, _ int) (int, http.Header) {
		for _, item := range coreauth.ProvimuxAnswered(marker, "provimux-test-claude-key") {
			inside = append(inside, item.Entry)
		}
		return 0, nil
	}
	request, _ := http.NewRequest(http.MethodPost, h.server.URL+"/v1/chat/completions", strings.NewReader(chatBody("provimux-test-claude-key", true, "")))
	request.Header.Set("Authorization", "Bearer "+sentClientKey)
	response, err := h.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	// The headers are read, the body not yet: the row already has the answer.
	if entry := response.Header.Get(logging.ProvimuxEntryHeader); entry != "anthropic/provimux-test-claude-sent" {
		t.Fatalf("fixture: the answer's entry %q", entry)
	}
	h.wantAnswered(sentClientKey, "provimux-test-claude-key", marker, []time.Time{clock.Now()}, "anthropic/provimux-test-claude-sent")
	_, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if len(inside) != 0 {
		t.Fatalf("the row had %v before the gateway stated the answer", inside)
	}

	// Later the same day, with a thinking suffix: the same item, its time moved.
	clock.add(time.Hour)
	h.stand.reset()
	h.checkSent(h.post("/v1/chat/completions", chatBody("provimux-test-claude-key(high)", false, ""), nil), key, "api.anthropic.com", "anthropic/provimux-test-claude-sent")
	h.wantAnswered(sentClientKey, "provimux-test-claude-key", marker, []time.Time{clock.Now()}, "anthropic/provimux-test-claude-sent")

	// An answer with no entry: an item whose entry is null.
	house := sentKeyFor(t, h, "provimux-test-house")
	h.stand.reset()
	h.checkSent(h.post("/v1/chat/completions", chatBody("provimux-test-house", false, ""), nil), house, "llm.provimux-test.example", "")
	h.wantAnswered(sentClientKey, "provimux-test-house", house.ProvimuxAccountMarker(), []time.Time{clock.Now()}, "null")
}

// 1. The requested model reaches the record on every path the matrix answers on (chat, stream, Claude's protocol, a
// token count, Responses, Gemini's path, every executor): after each answer the newest item of the row of the asked
// name is the answer's entry, or null.
func TestProvimuxAnsweredEveryPathOfTheMatrix(t *testing.T) {
	h := newSentHarness(t, sentMatrixConfig)
	clock := newSentClock()
	useAnsweredBook(t, clock, noAnsweredStore)
	logins := sentLogins(t, h)
	logins["copilot"] = registerCopilotRoutingTestAuth(t, h.service.coreManager, "", "provimux-test-copilot")
	copilotTestPublisher(t)(append(registry.GetStaticModelDefinitionsByChannel("copilot"), &registry.ModelInfo{ID: "provimux-test-copilot"}))
	h.service.registerExecutorForAuth(logins["copilot"], false)
	if !h.service.refreshModelRegistrationForAuth(logins["copilot"]) {
		t.Fatal("fixture: Copilot's models")
	}
	for _, tc := range sentMatrix() {
		t.Run(tc.name, func(t *testing.T) {
			h.t = t
			h.stand.reset()
			clock.add(time.Minute)
			account := logins[tc.login]
			if tc.login == "" {
				account = sentKeyFor(t, h, tc.model)
			}
			answer := h.post(tc.path, tc.body, nil)
			entry := answer.get(logging.ProvimuxEntryHeader)
			if answer.status != http.StatusOK || answer.get(logging.ProvimuxAccountHeader) == "" {
				t.Fatalf("fixture: %d %s", answer.status, answer.body)
			}
			items := coreauth.ProvimuxAnswered(account.ProvimuxAccountMarker(), tc.model)
			if len(items) == 0 || items[0].Entry != entry || !items[0].At.Equal(clock.Now()) {
				t.Fatalf("the row of %s: %+v, want %q first at %s", tc.model, items, entry, clock.Now())
			}
		})
	}
}

// 1. Finding 15's rule by header: one account and one name, two models sent, two items, newest first; the first again
// moves first.
func TestProvimuxAnsweredTwoEntriesNewestFirst(t *testing.T) {
	const native, alternate = "provimux-test-native", "provimux-test-alternate"
	h := newSentHarness(t, `payload:
  override:
    - models:
        - name: `+native+`
          headers:
            X-Client-Tier: special
      params:
        model: `+alternate+`
`)
	clock := newSentClock()
	useAnsweredBook(t, clock, noAnsweredStore)
	account := registerCopilotRoutingTestAuth(t, h.service.coreManager, "", native, alternate)
	copilotTestPublisher(t)(append(registry.GetStaticModelDefinitionsByChannel("copilot"), &registry.ModelInfo{ID: native}, &registry.ModelInfo{ID: alternate}))
	h.service.registerExecutorForAuth(account, false)
	if !h.service.refreshModelRegistrationForAuth(account) {
		t.Fatal("fixture: Copilot's models")
	}
	route, marker := "copilot/"+native, account.ProvimuxAccountMarker()
	var times []time.Time
	for _, tier := range []string{"normal", "special"} {
		clock.add(time.Hour)
		h.stand.reset()
		h.post("/v1/chat/completions", chatBody(route, false, ""), sentHeader("X-Client-Tier", tier))
		times = append([]time.Time{clock.Now()}, times...)
	}
	h.wantAnswered(sentClientKey, route, marker, times, "github-copilot/"+alternate, "github-copilot/"+native)
	clock.add(time.Hour)
	h.post("/v1/chat/completions", chatBody(route, false, ""), sentHeader("X-Client-Tier", "normal"))
	h.wantAnswered(sentClientKey, route, marker, []time.Time{clock.Now(), times[0]}, "github-copilot/"+native, "github-copilot/"+alternate)
}

// The items of one name: four outcomes at most, a fifth drops the oldest; one outcome again moves up; an item older
// than 90 days is gone (on reading and from what is kept).
func TestProvimuxAnsweredItemsAndExpiry(t *testing.T) {
	clock := newSentClock()
	store := boltAnsweredStore(t, filepath.Join(t.TempDir(), "provimux-test.db"))
	book := newProvimuxAnsweredBook(clock.Now, store)
	const marker, name = "a.aaaaaaaaaaaaaaaaaaaa", "provimux-test-model"
	read := func() string {
		var out []string
		for _, item := range book.read(marker, name) {
			out = append(out, item.Entry)
		}
		return strings.Join(out, ",")
	}
	for _, entry := range []string{"p/one", "p/two", "", "p/three", "p/four"} {
		clock.add(time.Hour)
		book.note(marker, name, entry)
	}
	if got := read(); got != "p/four,p/three,,p/two" {
		t.Fatalf("five outcomes: %q, want the four newest", got)
	}
	clock.add(time.Hour)
	book.note(marker, strings.ToUpper(name), "p/two")
	if got := read(); got != "p/two,p/four,p/three," {
		t.Fatalf("an outcome again, its name in another case: %q", got)
	}
	// 89 days later all is there; past 90 days after an item's last answer it is gone.
	clock.add(89 * 24 * time.Hour)
	if got := read(); got != "p/two,p/four,p/three," {
		t.Fatalf("after 89 days: %q", got)
	}
	book.note(marker, name, "p/four")
	// p/two's last answer was two hours after p/three's and three after the none's: 90 days after p/three's, those
	// two are gone and p/two is not; two hours later it is.
	clock.add(23 * time.Hour)
	if got := read(); got != "p/four,p/two" {
		t.Fatalf("90 days after the others' last answer: %q", got)
	}
	clock.add(2 * time.Hour)
	if got := read(); got != "p/four" {
		t.Fatalf("p/two past 90 days: %q", got)
	}
	// What the store keeps drops them too.
	book.flush()
	again := newProvimuxAnsweredBook(clock.Now, store)
	if got := again.read(marker, name); len(got) != 1 || got[0].Entry != "p/four" {
		t.Fatalf("kept: %+v", got)
	}
}

// 2. A restart keeps the record: what one run noted, the next reads from the local store; a run without a store
// keeps it for the run only and says so once.
func TestProvimuxAnsweredRestartKeepsIt(t *testing.T) {
	clock := newSentClock()
	path := filepath.Join(t.TempDir(), "provimux-test.db")
	const marker = "a.bbbbbbbbbbbbbbbbbbbb"
	first := newProvimuxAnsweredBook(clock.Now, boltAnsweredStore(t, path))
	first.note(marker, "provimux-test-a", "p/a")
	clock.add(time.Minute)
	first.note(marker, "provimux-test-a", "")
	clock.add(time.Minute)
	first.note(marker, "provimux-test-b", "p/b") // the writer writes on its own; the flush is the stop's
	first.flush()
	// The process ends: the store is closed and opened again by the next.
	stored, _ := first.store()
	_ = stored.(*kvstore.BoltStore).Close()
	second := newProvimuxAnsweredBook(clock.Now, boltAnsweredStore(t, path))
	if got := second.read(marker, "provimux-test-a"); len(got) != 2 || got[0].Entry != "" || got[1].Entry != "p/a" || !got[0].At.Equal(clock.Now().Add(-time.Minute)) {
		t.Fatalf("after the restart: %+v", got)
	}
	// A note of the new run before its first read is merged with what the store kept, not put in its place.
	clock.add(time.Minute)
	next := newProvimuxAnsweredBook(clock.Now, second.store)
	next.note(marker, "provimux-test-b", "p/c")
	next.flush()
	third := newProvimuxAnsweredBook(clock.Now, second.store)
	if got := third.read(marker, "provimux-test-b"); len(got) != 2 || got[0].Entry != "p/c" || got[1].Entry != "p/b" {
		t.Fatalf("merged: %+v", got)
	}
	// No store: kept for the run.
	alone := newProvimuxAnsweredBook(clock.Now, noAnsweredStore)
	alone.note(marker, "provimux-test-a", "p/a")
	alone.flush()
	if got := alone.read(marker, "provimux-test-a"); len(got) != 1 {
		t.Fatalf("without a store, this run: %+v", got)
	}
	if !alone.logged.Load() {
		t.Fatal("without a store the log does not say so")
	}
}

// 2. On the wire: answers, then the service's stop (its store closes), then a new run on the same store and the same
// marker key: both lists give what the first run's answers said.
func TestProvimuxAnsweredRestartOnTheWire(t *testing.T) {
	clock := newSentClock()
	path := filepath.Join(t.TempDir(), "provimux-test.db")
	if err := kvstore.ConfigureLocal(true, path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = kvstore.ShutdownLocal() })
	h := newSentHarness(t, answeredClaudeConfig)
	useAnsweredBook(t, clock, provimuxLocalAnsweredStore)
	key := sentKeyFor(t, h, "provimux-test-claude-key")
	h.checkSent(h.post("/v1/chat/completions", chatBody("provimux-test-claude-key", false, ""), nil), key, "api.anthropic.com", "anthropic/provimux-test-claude-sent")
	at := clock.Now()
	provimuxFlushAnswered() // what the service's stop does before its store closes (shutdownKVStore)
	if err := kvstore.ShutdownLocal(); err != nil {
		t.Fatal(err)
	}
	if err := kvstore.ConfigureLocal(true, path); err != nil {
		t.Fatal(err)
	}
	clock.add(3 * time.Hour)
	useAnsweredBook(t, clock, provimuxLocalAnsweredStore) // the next process: nothing in memory
	h.wantAnswered(sentClientKey, "provimux-test-claude-key", key.ProvimuxAccountMarker(), []time.Time{at}, "anthropic/provimux-test-claude-sent")
}

// 5. The marker and the record agree: another account's login in the same file, or a lost marker key, gives another
// marker, and its row starts with nothing.
func TestProvimuxAnsweredAnotherMarkerStartsEmpty(t *testing.T) {
	h := newSentHarness(t, "")
	clock := newSentClock()
	useAnsweredBook(t, clock, noAnsweredStore)
	login := func(account string) *coreauth.Auth {
		return h.register(&coreauth.Auth{ID: "provimux-test-claude-file.json", FileName: "provimux-test-claude-file.json", Provider: "claude", Status: coreauth.StatusActive,
			Attributes: map[string]string{"auth_kind": "oauth", "path": "/provimux-test/auths/provimux-test-claude-file.json"},
			Metadata: map[string]any{"type": "claude", "access_token": "provimux-test-token-" + account, "expired": sentFar(),
				"organization_uuid": "0f0e0d0c-0b0a-4909-8807-060504030201", "account_uuid": account}}, "provimux-test-claude-file")
	}
	first := login("1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d")
	h.checkSent(h.post("/v1/messages", `{"model":"provimux-test-claude-file","max_tokens":8,"messages":[{"role":"user","content":"provimux-test"}]}`, nil), first, "api.anthropic.com", "claude-subscription/provimux-test-claude-file")
	h.wantAnswered(sentClientKey, "provimux-test-claude-file", first.ProvimuxAccountMarker(), []time.Time{clock.Now()}, "claude-subscription/provimux-test-claude-file")

	taken := login("2b3c4d5e-6f7a-4b8c-9d0e-1f2a3b4c5d6e")
	if taken.ProvimuxAccountMarker() == first.ProvimuxAccountMarker() {
		t.Fatal("fixture: another account's login has the same marker")
	}
	h.wantAnswered(sentClientKey, "provimux-test-claude-file", taken.ProvimuxAccountMarker(), nil)

	coreauth.SetProvimuxMarkerKey([]byte("provimux-test-another-marker-k32"))
	h.wantAnswered(sentClientKey, "provimux-test-claude-file", taken.ProvimuxAccountMarker(), nil)
}

// 3 and 4. Two client keys under "serve only my names": both see the same items on a served name's row, whoever's
// answer made them (the record is per gateway); a name that is not served is on neither list and brings no item.
func TestProvimuxAnsweredTwoKeysUnderOnlyMyNames(t *testing.T) {
	h := newSentHarness(t, answeredClaudeConfig+`models:
  only-my-names:
    enabled: true
`)
	clock := newSentClock()
	useAnsweredBook(t, clock, noAnsweredStore)
	key := sentKeyFor(t, h, "provimux-test-claude-key")
	marker := key.ProvimuxAccountMarker()
	h.post("/v1/chat/completions", chatBody("provimux-test-claude-key", false, ""), sentHeader("Authorization", "Bearer "+sentOtherClientKey))
	for _, clientKey := range []string{sentClientKey, sentOtherClientKey} {
		h.wantAnswered(clientKey, "provimux-test-claude-key", marker, []time.Time{clock.Now()}, "anthropic/provimux-test-claude-sent")
	}
	// The upstream id behind the alias is not served: no row, and an answer for it is refused before any is noted.
	if answer := h.post("/v1/chat/completions", chatBody("provimux-test-claude-sent", false, ""), nil); answer.status == http.StatusOK {
		t.Fatalf("fixture: the unserved name was answered: %s", answer.body)
	}
	if items := coreauth.ProvimuxAnswered(marker, "provimux-test-claude-sent"); len(items) != 0 {
		t.Fatalf("an unserved name has items: %+v", items)
	}
	if client, management, raw := h.listsRow(sentClientKey, "provimux-test-claude-sent", marker); client != nil || management != nil {
		t.Fatalf("an unserved name is listed: %s", raw)
	}
}

// 2. Off and in Home mode nothing is noted or read: the lists carry neither account nor answered (W2's goldens, in
// test/provimux, hold them byte for byte), and what was noted while on stays out of them.
func TestProvimuxAnsweredOffAndHomeMode(t *testing.T) {
	h := newSentHarness(t, answeredClaudeConfig)
	clock := newSentClock()
	book := useAnsweredBook(t, clock, noAnsweredStore)
	key := sentKeyFor(t, h, "provimux-test-claude-key")
	h.post("/v1/chat/completions", chatBody("provimux-test-claude-key", false, ""), nil)
	marker := key.ProvimuxAccountMarker()
	if len(book.read(marker, "provimux-test-claude-key")) != 1 {
		t.Fatal("fixture: nothing noted while on")
	}
	for _, mode := range []string{"off", "Home mode"} {
		cfg := &config.Config{}
		if mode == "off" {
			off := false
			cfg.Models.AnsweredBy = &off
		} else {
			cfg.Home.Enabled = true
		}
		provimuxSetAnsweredBy(cfg)
		if logging.ProvimuxAnsweredBy() {
			t.Fatalf("%s: the switch is on", mode)
		}
		provimuxAnsweredNote(marker, key, "provimux-test-claude-key", "p/noted-while-off")
		h.post("/v1/chat/completions", chatBody("provimux-test-claude-key", false, ""), nil)
		if items := coreauth.ProvimuxAnswered(marker, "provimux-test-claude-key"); items != nil {
			t.Fatalf("%s: read %+v", mode, items)
		}
		_, _, raw := h.listsRow(sentClientKey, "provimux-test-claude-key", "")
		if strings.Contains(raw, `"answered"`) || strings.Contains(raw, `"account"`) {
			t.Fatalf("%s: the list carries the record: %s", mode, raw)
		}
		logging.SetProvimuxAnsweredBy(true)
	}
	if got := book.read(marker, "provimux-test-claude-key"); len(got) != 1 || got[0].Entry != "anthropic/provimux-test-claude-sent" {
		t.Fatalf("noted while off or in Home mode: %+v", got)
	}
}

// slowAnsweredStore is a store whose writes wait until released.
type slowAnsweredStore struct {
	release chan struct{}
	writes  atomic.Int32
}

func (s *slowAnsweredStore) KVGet(context.Context, string) ([]byte, bool, error) {
	return nil, false, nil
}
func (s *slowAnsweredStore) KVSet(context.Context, string, []byte, kvstore.KVSetOptions) (bool, error) {
	<-s.release
	s.writes.Add(1)
	return true, nil
}

// Many answers at once: no item is lost, none duplicated, and an answer never waits on the store: with the store's
// writes held, every note returns. What a note costs an answer is measured and logged.
func TestProvimuxAnsweredManyAtOnce(t *testing.T) {
	clock := newSentClock()
	slow := &slowAnsweredStore{release: make(chan struct{})}
	book := newProvimuxAnsweredBook(clock.Now, func() (provimuxAnsweredStore, error) { return slow, nil })
	const markers, names, rounds = 8, 25, 40
	var wg sync.WaitGroup
	started := time.Now()
	for m := 0; m < markers; m++ {
		for n := 0; n < names; n++ {
			wg.Add(1)
			go func(m, n int) {
				defer wg.Done()
				for r := 0; r < rounds; r++ {
					book.note(fmt.Sprintf("a.%020d", m), fmt.Sprintf("provimux-test-%d", n), fmt.Sprintf("p/%d", r%3))
				}
			}(m, n)
		}
	}
	wg.Wait()
	elapsed := time.Since(started)
	notes := markers * names * rounds
	for m := 0; m < markers; m++ {
		for n := 0; n < names; n++ {
			got := book.read(fmt.Sprintf("a.%020d", m), fmt.Sprintf("provimux-test-%d", n))
			if len(got) != 3 {
				t.Fatalf("marker %d name %d: %+v, want the three outcomes once each", m, n, got)
			}
		}
	}
	if slow.writes.Load() != 0 {
		t.Fatal("fixture: a write passed the held store")
	}
	close(slow.release)
	book.flush()
	if slow.writes.Load() == 0 {
		t.Fatal("nothing was written once the store answered")
	}
	t.Logf("%d notes from %d goroutines with the store's writes held: %s, %s a note", notes, markers*names, elapsed, elapsed/time.Duration(notes))
}

// What noting costs an answer: the conductor's call, its name looked up in the registry and the book's in-memory
// update, with the writer woken.
func BenchmarkProvimuxAnsweredNote(b *testing.B) {
	clock := newSentClock()
	book := newProvimuxAnsweredBook(clock.Now, noAnsweredStore)
	was := provimuxAnswered
	provimuxAnswered = book
	defer func() { provimuxAnswered = was }()
	wasOn := logging.SetProvimuxAnsweredBy(true)
	defer logging.SetProvimuxAnsweredBy(wasOn)
	auth := &coreauth.Auth{ID: "provimux-bench-auth", Provider: "claude"}
	models := make([]*registry.ModelInfo, 0, 200)
	for i := 0; i < 200; i++ {
		models = append(models, &registry.ModelInfo{ID: fmt.Sprintf("provimux-bench-%d", i)})
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, models)
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		provimuxAnsweredNote("a.cccccccccccccccccccc", auth, "provimux-bench-199", "p/bench")
	}
}

// TestProvimuxAnsweredNoteCost runs the benchmark once and logs it, so the figure is in the test's output.
func TestProvimuxAnsweredNoteCost(t *testing.T) {
	result := testing.Benchmark(BenchmarkProvimuxAnsweredNote)
	t.Logf("a note (registry lookup among 200 ids, in-memory update): %s/op, %d allocs/op, %d B/op", time.Duration(result.NsPerOp()), result.AllocsPerOp(), result.AllocedBytesPerOp())
}
