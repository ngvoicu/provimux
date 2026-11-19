# "Last served": how it is recorded with usage statistics off, and what it costs

`last_served_at` on a route of model-routes: the last time that login or key completed an upstream attempt for that model without failing.

## 1. Nothing stores it today

| Candidate | Why it does not serve | Evidence |
|---|---|---|
| The usage queue | reading it removes the records, and it is empty when `usage-statistics-enabled` is off | `internal/api/handlers/management/usage.go:23-36` (`PopOldest`); `internal/redisqueue/plugin.go:26` |
| `X-CPA-AUTH-KIND` | a response header to the client; nothing is kept | `internal/logging/cpa_trace.go:16-18`, `:221` |
| `ModelState.UpdatedAt` of a credential | written on a success, but also on every failure, reset and alias migration, so it is "last change", not "last served" | `sdk/cliproxy/auth/conductor_cooldown.go:2591-2608` (`:2607`) |
| `Auth.LastInferenceVerifiedAt` | per credential, not per model; cleared when the credential is replaced | `conductor_cooldown.go:1644-1647`; `conductor_refresh.go:700`, `:754`, `:777` |
| `Auth.Success` and the recent-requests ring | counts per credential in 10-minute buckets, no model | `conductor_cooldown.go:1616-1621`; `sdk/cliproxy/auth/types.go:326-337` |
| The auth manager's result hook (`Hook.OnResult`) | one hook, fixed when the manager is built, and the builder passes none; chaining one in means editing `builder.go` and the manager | `sdk/cliproxy/auth/conductor.go:152-159`, `:276`; `sdk/cliproxy/builder.go:311` |

## 2. The usage record stream is always on

Every upstream attempt publishes one usage record, whatever the setting:

- The executors' reporter publishes exactly one record per attempt, success or failure (`internal/runtime/executor/helps/usage_helpers.go:637-645`, `:666-673`, `:682-688`).
- `usage.Manager.Publish` puts it on the queue with no check of any setting (`sdk/cliproxy/usage/manager.go:728-782`), and one goroutine hands each record to every registered plugin (`:784-816`).
- `usage-statistics-enabled` is read in one place: the plugin that fills the usage queue returns early (`internal/redisqueue/plugin.go:26`; the switch itself `internal/redisqueue/usage_toggle.go:5-16`). The config comment says the same: "toggles in-memory usage aggregation; when false, usage data is discarded" (`internal/config/config.go:92-93`).

**[ran]** With `redisqueue.SetUsageStatisticsEnabled(false)`, a plugin registered through `coreusage.RegisterNamedPlugin` received both a success record (`NewUsageReporter(...).EnsurePublished`) and a failure record (`.PublishFailure`). The success record carried `AuthID="probe-auth"`, `AuthIndex="a8d167b7d82c8c15"`, `Model="claude-upstream"`, `Alias="team/alias-model(high)"`, `AuthType="oauth"`, `Failed=false`. This settles a point the earlier research on the console had left as an inference.

A record names both models: `Model` is the upstream model the executor called, `Alias` is the model as the client asked for it (`usage_helpers.go:105-108`, `:759-760`), which the conductor sets from the route model (`sdk/cliproxy/auth/conductor_execution.go:2062-2064`). `Alias` is therefore the registry's model ID plus, at most, a thinking suffix such as `(high)`.

## 3. The recorder

A plugin of the layer, in a new file of the management package, registered once:

```go
// internal/api/handlers/management/provimux_last_served.go (new file, package management)

// provimuxLastServed keeps, per credential and model, when an upstream attempt last succeeded.
// It holds timestamps only, in memory.
type provimuxLastServed struct {
	mu     sync.Mutex
	now    func() time.Time
	since  time.Time                   // when recording started
	byAuth map[string]map[string]int64 // auth ID -> model key -> unix nanoseconds
}

func newProvimuxLastServed(now func() time.Time) *provimuxLastServed

// HandleUsage implements coreusage.Plugin.
func (r *provimuxLastServed) HandleUsage(ctx context.Context, record coreusage.Record)

// servedAt returns the model keys of one credential with their times.
func (r *provimuxLastServed) servedAt(authID string) map[string]time.Time

// prune forgets credentials that no longer exist.
func (r *provimuxLastServed) prune(keep map[string]struct{})

// provimuxModelKey is the key a model is recorded and looked up under.
func provimuxModelKey(model string) string

var provimuxLastServedRecorder = newProvimuxLastServed(time.Now)

func init() { coreusage.RegisterNamedPlugin("provimux:last-served", provimuxLastServedRecorder) }
```

Rules:

| Rule | Detail |
|---|---|
| What counts | a record with `Failed == false`, `AuthID != ""` and `coreusage.GenerateEnabled(record.Generate)` (`manager.go:72`, `:621-626`): a completed generation attempt. Failures and token counts do not move the time |
| Key | `provimuxModelKey(record.Alias)`, falling back to `record.Model` when the alias is empty: thinking suffix cut with `thinking.ParseSuffix` (`internal/thinking/suffix.go:23`), trimmed, lower-case. The handler looks a route up with `provimuxModelKey(model.ID)` |
| Time | `now()` when the record is handled, which is at the end of the attempt |
| Since | `since` is set when the recorder is made, that is at process start; the response gives it as `last_served.recording_since` |
| Forgetting | the model-routes handler calls `prune` with the credentials it just listed, so a removed login takes its entries with it |
| Bound | at most 4,096 model keys per credential; past that, new keys are not added. A key is only written for an attempt that succeeded, so the set is bounded by the models the credential really serves; the cap is a guard, not a working limit |
| Name | registered by name, so a second registration replaces the first instead of doubling it (`manager.go:703-724`) |

`now` is injected so tests set the clock (AGENTS.md: no wall-clock sleeps in such tests).

## 4. Cost

| | Cost | Basis |
|---|---|---|
| Request path | **none added.** Publishing a record, the queue append and the wake of the dispatcher already happen for every attempt, and the queue plugin is always registered | `manager.go:774-781`; `internal/redisqueue/plugin.go:16-18` |
| Per upstream attempt | **84 ns, 0 allocations**, on the dispatcher goroutine: a suffix parse, a lower-case, one map write under a mutex | [ran] benchmark of that path on an Apple M4 Pro, 29 million iterations |
| Other plugins | the dispatcher calls plugins one after another (`manager.go:802-816`), so a slow plugin delays the rest. This one holds its mutex for one map write and does no I/O | |
| Memory | one `int64` and one key string per credential and model served since start. 20 credentials × 150 models is about 3,000 entries, a few hundred kilobytes at most | |
| Disk, network, logs | none | |
| Reading | the handler copies one credential's map under the mutex per credential | |

## 5. What it means for someone who turned usage statistics off

Turning statistics off stops CPA from keeping usage data. The recorder keeps, per login and model, the time of the last success and nothing else: no token counts, no client API key, no request or session ID, no content, no history. It is never written to disk and is gone at restart. It is visible only through this endpoint, behind the management key.

It cannot be switched off separately. A switch would be a new config key, which means edits in the config types, the v8 mapping and the example file; I do not think one timestamp per route earns that. If the owner wants the switch, the natural rule is "record only while `usage-statistics-enabled` is on", readable from `redisqueue.UsageStatisticsEnabled()` with no new key; the cost is that the column is empty for exactly the users this task names.

## 6. Limits of the value

- **Since start only.** After a restart every route reads `null` until it serves again; `recording_since` lets the console say "not since 07:00" instead of "never".
- **Success means the attempt was not reported as failed.** A stream the client aborted counts if the executor did not report a failure.
- **Per route model, not per upstream model.** For an alias that stands for several upstream models, the time is the alias's.
- **Home mode** publishes records too (`sdk/cliproxy/auth/home_result.go:57`), but management endpoints do not exist there.
