package cliproxy

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Provimux: what each credential's answers said to each client key, on both lists (answered-by.md 4.4 and 8.4, step
// W6b, as amended by the lead on 2026-11-27). Every key, login and id is invented; every time is on a clock the test
// sets.

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

// useAnsweredBook puts a record of the test's own in the gateway's place, on clock, without a file, for the test.
func useAnsweredBook(t *testing.T, clock *sentClock) *provimuxAnsweredBook {
	t.Helper()
	book := newProvimuxAnsweredBook(clock.Now)
	was := provimuxAnswered
	provimuxAnswered = book
	t.Cleanup(func() { provimuxAnswered = was })
	return book
}

// answeredRef and answeredCred are the record's references of a client key and a credential id.
func answeredRef(key string) string { return coreauth.ProvimuxClientRef(key) }
func answeredCred(id string) string { return coreauth.ProvimuxCredentialRef(id) }
func answeredKeyOn(t *testing.T) {
	t.Helper()
	coreauth.SetProvimuxMarkerKey([]byte("provimux-test-sent-marker-key-32"))
	was := logging.SetProvimuxAnsweredBy(true)
	t.Cleanup(func() {
		logging.SetProvimuxAnsweredBy(was)
		coreauth.SetProvimuxMarkerKey(nil)
	})
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

// wantAnswered checks both lists' row of marker under model: the client list of clientKey with days, the management
// list (every key's record together) with times, the same entries in the same order: for a test in which only
// clientKey's answers made the row. Items are "entry@" (null for none); the day and time are the clock's.
func (h *sentHarness) wantAnswered(clientKey, model, marker string, at []time.Time, entries ...string) {
	h.t.Helper()
	h.wantAnsweredLists(clientKey, model, marker, at, entries, at, entries)
}

// wantAnsweredLists checks the client list of clientKey against clientAt/clientEntries and the management list
// against managementAt/managementEntries.
func (h *sentHarness) wantAnsweredLists(clientKey, model, marker string, clientAt []time.Time, clientEntries []string, managementAt []time.Time, managementEntries []string) {
	h.t.Helper()
	client, management, raw := h.listsRow(clientKey, model, marker)
	if client == nil || management == nil {
		h.t.Fatalf("no row of %s under %s (client %v, management %v): %s", marker, model, client, management, raw)
	}
	var wantClient, wantManagement []string
	for i, entry := range clientEntries {
		// date-safe: Go's layout, not a date; the times are the test's clock's.
		wantClient = append(wantClient, entry+"@"+clientAt[i].UTC().Format("2006-01-02"))
	}
	for i, entry := range managementEntries {
		// date-safe: Go's layout, not a date; the times are the test's clock's.
		wantManagement = append(wantManagement, entry+"@"+managementAt[i].UTC().Format("2006-01-02T15:04:05.000Z07:00"))
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

// 1, 2. An answer makes its row's first item once it has been written to its client, on both lists; before the
// gateway writes it (read from inside the stand-in) the row has none. An answer with no entry is an item of its own,
// null. A name called with a thinking suffix lands on the row whose id the credential registers.
func TestProvimuxAnsweredFirstAnswerMakesTheRow(t *testing.T) {
	h := newSentHarness(t, answeredClaudeConfig)
	clock := newSentClock()
	useAnsweredBook(t, clock)
	key := sentKeyFor(t, h, "provimux-test-claude-key")
	marker := key.ProvimuxAccountMarker()

	h.wantAnswered(sentClientKey, "provimux-test-claude-key", marker, nil)
	var inside []string
	h.stand.status = func(r *http.Request, _ int) (int, http.Header) {
		for _, item := range coreauth.ProvimuxAnswered(answeredRef(sentClientKey), key.ID, marker, "provimux-test-claude-key") {
			inside = append(inside, item.Entry)
		}
		return 0, nil
	}
	answer := h.post("/v1/chat/completions", chatBody("provimux-test-claude-key", true, ""), nil)
	if entry := answer.get(logging.ProvimuxEntryHeader); entry != "anthropic/provimux-test-claude-sent" {
		t.Fatalf("fixture: the answer's entry %q", entry)
	}
	h.wantAnswered(sentClientKey, "provimux-test-claude-key", marker, []time.Time{clock.Now()}, "anthropic/provimux-test-claude-sent")
	if len(inside) != 0 {
		t.Fatalf("the row had %v before the gateway wrote the answer", inside)
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

// 3. The name called reaches the record on every path the matrix answers on (chat, stream, Claude's protocol, a token
// count, Responses, Gemini's path, every executor): after each answer the newest item of the called name's row for the
// caller is the answer's entry, or null.
func TestProvimuxAnsweredEveryPathOfTheMatrix(t *testing.T) {
	h := newSentHarness(t, sentMatrixConfig)
	clock := newSentClock()
	useAnsweredBook(t, clock)
	logins := sentLogins(t, h)
	logins["copilot"] = sentCopilot(t, h, "provimux-test-copilot")
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
			items := coreauth.ProvimuxAnswered(answeredRef(sentClientKey), account.ID, account.ProvimuxAccountMarker(), tc.model)
			if len(items) == 0 || items[0].Entry != entry || !items[0].At.Equal(clock.Now()) {
				t.Fatalf("the row of %s: %+v, want %q first at %s", tc.model, items, entry, clock.Now())
			}
		})
	}
}

// 1. Finding 15's rule by header: one credential and one name, two models sent, two items, newest first; the first
// again moves first.
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
	useAnsweredBook(t, clock)
	account := sentCopilot(t, h, native, alternate)
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

// The items of one row and client: four outcomes at most, a fifth drops the oldest; one outcome again moves up;
// names are exact (no case folding); an item past 90 days is not shown, and is gone from the file at the next write.
func TestProvimuxAnsweredItemsAndExpiry(t *testing.T) {
	clock := newSentClock()
	dir := t.TempDir()
	book := newProvimuxAnsweredBook(clock.Now)
	book.setPlace(dir)
	const client, cred, marker, name = "k.client", "c.credential", "a.aaaaaaaaaaaaaaaaaaaa", "provimux-test-model"
	read := func(b *provimuxAnsweredBook, name string) string {
		var out []string
		for _, item := range b.read(client, false, cred, marker, name) {
			out = append(out, item.Entry)
		}
		return strings.Join(out, ",")
	}
	for _, entry := range []string{"p/one", "p/two", "", "p/three", "p/four"} {
		clock.add(time.Hour)
		book.note(client, cred, marker, name, entry)
	}
	if got := read(book, name); got != "p/four,p/three,,p/two" {
		t.Fatalf("five outcomes: %q, want the four newest", got)
	}
	clock.add(time.Hour)
	book.note(client, cred, marker, strings.ToUpper(name), "p/two")
	if got := read(book, name); got != "p/four,p/three,,p/two" {
		t.Fatalf("a name in another case is another row: %q", got)
	}
	book.note(client, cred, marker, name, "p/two")
	if got := read(book, name); got != "p/two,p/four,p/three," {
		t.Fatalf("an outcome again: %q", got)
	}
	clock.add(89 * 24 * time.Hour)
	if got := read(book, name); got != "p/two,p/four,p/three," {
		t.Fatalf("after 89 days: %q", got)
	}
	book.note(client, cred, marker, name, "p/four")
	clock.add(23 * time.Hour)
	if got := read(book, name); got != "p/four,p/two" {
		t.Fatalf("90 days after the others' last answer: %q", got)
	}
	book.close()
	raw, err := os.ReadFile(filepath.Join(dir, provimuxAnsweredFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "p/three") || !strings.Contains(string(raw), "p/two") {
		t.Fatalf("the file after the next write: %s", raw)
	}
	again := newProvimuxAnsweredBook(clock.Now)
	again.setPlace(dir)
	if got := read(again, name); got != "p/four,p/two" {
		t.Fatalf("kept: %q", got)
	}
}

// Restart: what one run noted, the next reads from the file; without a place it is kept for the run, said once.
func TestProvimuxAnsweredRestartKeepsIt(t *testing.T) {
	clock := newSentClock()
	dir := t.TempDir()
	const cred, marker = "c.credential", "a.bbbbbbbbbbbbbbbbbbbb"
	first := newProvimuxAnsweredBook(clock.Now)
	first.setPlace(dir)
	first.note("k.one", cred, marker, "provimux-test-a", "p/a")
	clock.add(time.Minute)
	first.note("k.one", cred, marker, "provimux-test-a", "")
	clock.add(time.Minute)
	first.note("k.two", cred, marker, "provimux-test-a", "p/b")
	first.close()
	second := newProvimuxAnsweredBook(clock.Now)
	second.setPlace(dir)
	if got := second.read("k.one", false, cred, marker, "provimux-test-a"); len(got) != 2 || got[0].Entry != "" || got[1].Entry != "p/a" || !got[0].At.Equal(clock.Now().Add(-time.Minute)) {
		t.Fatalf("after the restart, client one: %+v", got)
	}
	if got := second.read("k.two", false, cred, marker, "provimux-test-a"); len(got) != 1 || got[0].Entry != "p/b" {
		t.Fatalf("after the restart, client two: %+v", got)
	}
	if got := second.read("", true, cred, marker, "provimux-test-a"); len(got) != 3 || got[0].Entry != "p/b" {
		t.Fatalf("after the restart, every client: %+v", got)
	}
	alone := newProvimuxAnsweredBook(clock.Now)
	alone.note("k.one", cred, marker, "provimux-test-a", "p/a")
	alone.close()
	if !alone.logged.Load() {
		t.Fatal("without a place the log does not say so")
	}
}

// On the wire: answers, the stop (close), a new run on the same auth folder: both lists give the first run's item.
func TestProvimuxAnsweredRestartOnTheWire(t *testing.T) {
	h := newSentHarness(t, answeredClaudeConfig)
	clock := newSentClock()
	dir := t.TempDir()
	useAnsweredBook(t, clock).setPlace(dir)
	key := sentKeyFor(t, h, "provimux-test-claude-key")
	h.checkSent(h.post("/v1/chat/completions", chatBody("provimux-test-claude-key", false, ""), nil), key, "api.anthropic.com", "anthropic/provimux-test-claude-sent")
	at := clock.Now()
	provimuxCloseAnswered()
	clock.add(3 * time.Hour)
	useAnsweredBook(t, clock).setPlace(dir) // the next process: nothing in memory
	h.wantAnswered(sentClientKey, "provimux-test-claude-key", key.ProvimuxAccountMarker(), []time.Time{at}, "anthropic/provimux-test-claude-sent")
}

// The marker and the record agree: another account's login in the same file, or a lost marker key, gives another
// marker, and its row starts with nothing.
func TestProvimuxAnsweredAnotherMarkerStartsEmpty(t *testing.T) {
	h := newSentHarness(t, "")
	clock := newSentClock()
	useAnsweredBook(t, clock)
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

// Per client key (finding 30, the lead's decision of 2026-11-27): one key's answer is on that key's list and on the
// management list, never on another key's; under "serve only my names" too. Callers without a key share a record of
// their own.
func TestProvimuxAnsweredPerClientKey(t *testing.T) {
	for _, only := range []bool{false, true} {
		t.Run(fmt.Sprint("only my names ", only), func(t *testing.T) {
			h := newSentHarness(t, answeredClaudeConfig+fmt.Sprintf("models:\n  only-my-names:\n    enabled: %v\n", only))
			clock := newSentClock()
			useAnsweredBook(t, clock)
			key := sentKeyFor(t, h, "provimux-test-claude-key")
			marker := key.ProvimuxAccountMarker()
			h.post("/v1/chat/completions", chatBody("provimux-test-claude-key", false, ""), sentHeader("Authorization", "Bearer "+sentOtherClientKey))
			at := []time.Time{clock.Now()}
			entry := []string{"anthropic/provimux-test-claude-sent"}
			h.wantAnsweredLists(sentOtherClientKey, "provimux-test-claude-key", marker, at, entry, at, entry)
			h.wantAnsweredLists(sentClientKey, "provimux-test-claude-key", marker, nil, nil, at, entry)
			if items := coreauth.ProvimuxAnswered("", key.ID, marker, "provimux-test-claude-key"); len(items) != 0 {
				t.Fatalf("a caller without a key sees another key's record: %+v", items)
			}
		})
	}
}

// 2. Off and in Home mode nothing is noted or read: the lists carry neither account nor answered (W2's goldens, in
// test/provimux, hold them byte for byte), and what was noted while on stays out of them.
func TestProvimuxAnsweredOffAndHomeMode(t *testing.T) {
	h := newSentHarness(t, answeredClaudeConfig)
	clock := newSentClock()
	book := useAnsweredBook(t, clock)
	key := sentKeyFor(t, h, "provimux-test-claude-key")
	h.post("/v1/chat/completions", chatBody("provimux-test-claude-key", false, ""), nil)
	marker := key.ProvimuxAccountMarker()
	ref, cred := answeredRef(sentClientKey), answeredCred(key.ID)
	if len(book.read(ref, false, cred, marker, "provimux-test-claude-key")) != 1 {
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
		logging.ProvimuxNoteWritten(logging.AuthStatement{Kind: "apikey", Provider: "claude", Provimux: logging.ProvimuxAnswer{Account: marker, Credential: key.ID, Name: "provimux-test-claude-key", Entry: "p/noted-while-off", Client: ref}})
		h.post("/v1/chat/completions", chatBody("provimux-test-claude-key", false, ""), nil)
		if items := coreauth.ProvimuxAnswered(ref, key.ID, marker, "provimux-test-claude-key"); items != nil {
			t.Fatalf("%s: read %+v", mode, items)
		}
		_, _, raw := h.listsRow(sentClientKey, "provimux-test-claude-key", "")
		if strings.Contains(raw, `"answered"`) || strings.Contains(raw, `"account"`) {
			t.Fatalf("%s: the list carries the record: %s", mode, raw)
		}
		logging.SetProvimuxAnsweredBy(true)
	}
	if got := book.read(ref, false, cred, marker, "provimux-test-claude-key"); len(got) != 1 || got[0].Entry != "anthropic/provimux-test-claude-sent" {
		t.Fatalf("noted while off or in Home mode: %+v", got)
	}
}

// Many answers at once: no item is lost, none duplicated, the list is newest first, and a note never waits for the
// file: with the writer held, every note returns. What a note costs an answer is measured and logged.
func TestProvimuxAnsweredManyAtOnce(t *testing.T) {
	clock := newSentClock()
	book := newProvimuxAnsweredBook(func() time.Time { clock.add(time.Millisecond); return clock.Now() })
	book.setPlace(t.TempDir())
	book.writing.Lock() // the writer is held for the whole of the notes
	const clients, names, rounds = 8, 25, 40
	var wg sync.WaitGroup
	started := time.Now()
	for c := 0; c < clients; c++ {
		for n := 0; n < names; n++ {
			wg.Add(1)
			go func(c, n int) {
				defer wg.Done()
				for r := 0; r < rounds; r++ {
					book.note(fmt.Sprintf("k.%d", c), "c.credential", "a.aaaaaaaaaaaaaaaaaaaa", fmt.Sprintf("provimux-test-%d", n), fmt.Sprintf("p/%d", r%3))
				}
			}(c, n)
		}
	}
	wg.Wait()
	elapsed := time.Since(started)
	book.writing.Unlock()
	notes := clients * names * rounds
	for c := 0; c < clients; c++ {
		for n := 0; n < names; n++ {
			got := book.read(fmt.Sprintf("k.%d", c), false, "c.credential", "a.aaaaaaaaaaaaaaaaaaaa", fmt.Sprintf("provimux-test-%d", n))
			if len(got) != 3 || !(got[0].At.After(got[1].At) && got[1].At.After(got[2].At)) {
				t.Fatalf("client %d name %d: %+v, want the three outcomes once each, newest first", c, n, got)
			}
		}
	}
	book.close()
	t.Logf("%d notes from %d goroutines with the writer held: %s, %s a note", notes, clients*names, elapsed, elapsed/time.Duration(notes))
}

// What noting costs an answer: the row's name looked up in the registry (where the statement is made) and the
// note where it is written (memory only, the writer woken).
func BenchmarkProvimuxAnsweredNote(b *testing.B) {
	clock := newSentClock()
	book := newProvimuxAnsweredBook(clock.Now)
	was := provimuxAnswered
	provimuxAnswered = book
	defer func() { provimuxAnswered = was }()
	wasOn := logging.SetProvimuxAnsweredBy(true)
	defer logging.SetProvimuxAnsweredBy(wasOn)
	coreauth.SetProvimuxMarkerKey([]byte("provimux-bench-marker-key-32byte"))
	defer coreauth.SetProvimuxMarkerKey(nil)
	auth := &coreauth.Auth{ID: "provimux-bench-auth", Provider: "claude"}
	models := make([]*registry.ModelInfo, 0, 200)
	for i := 0; i < 200; i++ {
		models = append(models, &registry.ModelInfo{ID: fmt.Sprintf("provimux-bench-%d", i)})
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, models)
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	client := coreauth.ProvimuxClientRef("provimux-bench-client-key")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		name := provimuxAnsweredName(auth.ID, "provimux-bench-199")
		logging.ProvimuxNoteWritten(logging.AuthStatement{Kind: "oauth", Provider: "claude", Provimux: logging.ProvimuxAnswer{Account: "a.cccccccccccccccccccc", Credential: auth.ID, Name: name, Entry: "p/bench", Client: client}})
	}
}

// TestProvimuxAnsweredNoteCost runs the benchmark once and logs it, so the figure is in the test's output.
func TestProvimuxAnsweredNoteCost(t *testing.T) {
	result := testing.Benchmark(BenchmarkProvimuxAnsweredNote)
	t.Logf("a note (the name among 200 ids, two keyed hashes, the in-memory update): %s/op, %d allocs/op, %d B/op", time.Duration(result.NsPerOp()), result.AllocsPerOp(), result.AllocedBytesPerOp())
}

var _ = json.Marshal
