package provimux

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/provimux/savings"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// savingsWaiter signals each record it receives. Registered after Savings' plugin, a signal means
// the dispatcher has given the record to Savings too.
type savingsWaiter chan struct{}

func (w savingsWaiter) HandleUsage(context.Context, coreusage.Record) {
	select {
	case w <- struct{}{}:
	default:
	}
}

var savingsSeen = make(savingsWaiter, 64)

func startSavings(t *testing.T) {
	t.Helper()
	registry.SetLocalModelCatalogs(true) // no download from openrouter.ai in these tests
	t.Cleanup(func() { registry.SetLocalModelCatalogs(false) })
	savings.Start()
	coreusage.RegisterNamedPlugin("provimux-savings-e2e-waiter", savingsSeen)
}

func publish(t *testing.T, records ...coreusage.Record) {
	t.Helper()
	for _, record := range records {
		coreusage.PublishRecord(context.Background(), record)
	}
	for range records {
		<-savingsSeen
	}
}

type savingsRow struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Requests int64  `json:"requests"`
	Tokens   struct {
		Input struct {
			Uncached   int64 `json:"uncached"`
			CacheRead  int64 `json:"cache_read"`
			CacheWrite int64 `json:"cache_write"`
			Write5m    int64 `json:"cache_write_5m"`
			Write1h    int64 `json:"cache_write_1h"`
			Total      int64 `json:"total"`
		} `json:"input"`
		Output struct {
			Total int64 `json:"total"`
		} `json:"output"`
	} `json:"tokens"`
	ReportedCost struct {
		USD float64 `json:"usd"`
	} `json:"reported_cost"`
}

type savingsAnswer struct {
	Totals     savingsRow `json:"totals"`
	Breakdowns []struct {
		Rows []savingsRow `json:"rows"`
	} `json:"breakdowns"`
}

func getSavings(t *testing.T, query string) savingsAnswer {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/provimux/savings?"+query, nil)
	(&management.Handler{}).ProvimuxSavings(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	var answer savingsAnswer
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	return answer
}

// With usage-statistics-enabled off, usage records still reach Savings' totals.
func TestProvimuxSavingsEndToEndStatisticsOff(t *testing.T) {
	startSavings(t)
	redisqueue.SetUsageStatisticsEnabled(false)
	before := getSavings(t, "period=today&model=e2e-model").Totals.Requests
	record := coreusage.Record{Provider: "claude", Model: "e2e-model", AuthType: "oauth",
		Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 5}}
	publish(t, record, record, record)
	if got := getSavings(t, "period=today&model=e2e-model").Totals.Requests - before; got != 3 {
		t.Fatalf("requests recorded %d, want 3", got)
	}
}

// Usage payloads go through each protocol's real parser, then the recorder: the input buckets add
// up to the upstream's input total as that provider counts it, and cache reads are its cached count.
func TestProvimuxSavingsRealParsers(t *testing.T) {
	startSavings(t)
	claudeWrite := helps.ParseClaudeUsage([]byte(`{"usage":{"input_tokens":100,"cache_creation_input_tokens":5000,"cache_read_input_tokens":0,"output_tokens":50}}`))
	if claudeWrite.CachedTokens != 5000 {
		t.Fatalf("premise: Claude's CachedTokens falls back to cache writes, got %d", claudeWrite.CachedTokens)
	}
	claudeRead := helps.ParseClaudeUsage([]byte(`{"usage":{"input_tokens":20,"cache_creation_input_tokens":0,"cache_read_input_tokens":3000,"output_tokens":40}}`))
	claudeSplit := helps.ParseClaudeUsage([]byte(`{"usage":{"input_tokens":10,"cache_creation_input_tokens":5000,"cache_read_input_tokens":200,"output_tokens":30,
		"cache_creation":{"ephemeral_5m_input_tokens":1000,"ephemeral_1h_input_tokens":4000}}}`))
	openAI := helps.ParseOpenAIUsage([]byte(`{"usage":{"prompt_tokens":1000,"completion_tokens":200,"prompt_tokens_details":{"cached_tokens":600},"cost":0.0123}}`))
	codex, ok := helps.ParseCodexUsage([]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":1500,"input_tokens_details":{"cached_tokens":700},"output_tokens":100,"output_tokens_details":{"reasoning_tokens":40}}}}`))
	if !ok {
		t.Fatal("codex usage not parsed")
	}
	gemini := helps.ParseGeminiUsage([]byte(`{"usageMetadata":{"promptTokenCount":900,"cachedContentTokenCount":400,"candidatesTokenCount":100,"thoughtsTokenCount":30}}`))

	for _, tc := range []struct {
		name, provider, model, kind string
		detail                      coreusage.Detail
		input, cached, write        int64
		write5m, write1h            int64
		reported                    float64
	}{
		{"claude write", "claude", "e2e-claude-write", "oauth", claudeWrite, 5100, 0, 5000, 0, 0, 0},
		{"claude read", "claude", "e2e-claude-read", "oauth", claudeRead, 3020, 3000, 0, 0, 0, 0},
		{"claude split", "claude", "e2e-claude-split", "oauth", claudeSplit, 5210, 200, 5000, 1000, 4000, 0},
		{"openrouter", "openrouter", "e2e-openai", "apikey", openAI, 1000, 600, 0, 0, 0, 0.0123},
		{"codex", "codex", "e2e-codex", "oauth", codex, 1500, 700, 0, 0, 0, 0},
		{"gemini", "gemini", "e2e-gemini", "oauth", gemini, 900, 400, 0, 0, 0, 0},
	} {
		publish(t, coreusage.Record{Provider: tc.provider, Model: tc.model, AuthType: tc.kind,
			BaseURL: "https://openrouter.ai/api/v1", Detail: tc.detail})
		row := getSavings(t, "period=today&model="+tc.model).Totals
		in := row.Tokens.Input
		if in.Uncached+in.CacheRead+in.CacheWrite != tc.input || in.Total != tc.input || in.CacheRead != tc.cached ||
			in.CacheWrite != tc.write || in.Write5m != tc.write5m || in.Write1h != tc.write1h {
			t.Errorf("%s: input %+v, want total %d, cached %d, write %d (%d/%d)", tc.name, in, tc.input, tc.cached, tc.write, tc.write5m, tc.write1h)
		}
		if row.ReportedCost.USD != tc.reported {
			t.Errorf("%s: reported cost %v, want %v", tc.name, row.ReportedCost.USD, tc.reported)
		}
	}
}
