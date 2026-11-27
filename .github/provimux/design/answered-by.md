# Who answered: an account marker and the answering model on every answer

Design, 2026-11-26, amended late that evening, and twice on 2026-11-27 by the lead (the two sections that follow, the later first). The contract a build is checked against is `prices-and-cache/openapi.yaml` (amended with this document: the schemas `Account`, `Answered`, `AnswerStatement` and the six headers) with `prices-and-cache/examples/answers.json` beside it.

## Amended 2026-11-27 (2): decided by the lead (only where the gateway as shipped makes every part)

The review of that amendment's build (`provimux-answered-entry` at `d21428393`, findings 32 to 43, each reproduced 50 times of 50) found more cases of a wrong entry or a lost or leaked record, nearly all from **embedding**: a program that uses the gateway's SDK with an executor, an HTTP client or transport, a response writer or an access provider of its own. Detecting every such arrangement had not converged. The lead changed the scope: **the entry and the record are stated only where the gateway as shipped makes every part itself, recognised positively; everywhere else there is none.** Each part is recognised by a mark Provimux's own code sets where the part is made, in memory, unexported: never an attribute, a name, a Go package or a declaration other code can set. This section is that decision as built; where it and anything below disagree, this one holds. The amendment before it (2026-11-27) still holds where this one does not replace it.

**A. The entry on an answer.** An answer names an entry (and `X-Provimux-Model-Sent`, `-Model`, `-Model-Basis`) only when all of this holds; where any is missing it names none, and one noted earlier in the attempt is cancelled:

1. **The executor is one of the gateway's built-in ones, wired by its own service.** (Replaced by part (5) below: one standing rule per provider; the record of registrations, their revisions and the comparison at publication are gone. The closed list stays.) `Service.registerExecutor`, the one place the service registers executors for the credentials of its own configuration and auth folder, records each registration in an unexported table per manager (`sdk/cliproxy/provimux_builtin_executors.go`): the executor, the manager's revision of that registration, and its kind on the closed list below, which is a type switch over types of `internal/runtime/executor` (no program outside this module can make one). When the answer is stated, the conductor's attempt context says which manager runs it (one line in `newUpstreamAttemptContext`), and the executor that manager publishes for the credential must be exactly the recorded one, at exactly its revision. An embedding's `ExecutorWrapper` (the registered executor is not the one the service made), any `Manager.RegisterExecutor` by other code (the revision moves), and a service registration that follows another program's or an unregistration (a gap in the revisions: an attempt may have started on the other one) all give none, until the service registers its own twice with nothing between. The functions that decide are installed once, while the program initialises (`SetProvimuxAnswerEntry` keeps the first call). Closed list: Codex (`CodexAutoExecutor`), Claude, Gemini and Gemini Interactions (`GeminiExecutor`), Vertex (`GeminiVertexExecutor`), Antigravity, Kimi, xAI (`XAIAutoExecutor`), Copilot, Devin, Meta, OpenAI-compatible (`OpenAICompatExecutor`). Not on it: AI Studio (a socket a browser opened), a plugin's executor, the plugin-refresh wrapper of an OpenAI-compatible key, anything an embedding registers. A test (`TestProvimuxBuiltinExecutorListIsClosed`) fails when the service makes an executor the list does not know.
2. **The HTTP client is built by the gateway over transports it made itself, recognised by identity.** Every client a built-in executor sends with is built by a helper of `internal/runtime/executor/helps` (protected; not changed), which applies Provimux's decorator after choosing a transport from three inputs: the per-credential transport on the context, Go's default transport, and the proxy settings of the configuration. Provimux recognises the first two by identity: the builder hands the manager its default per-credential provider through `provimuxGatewayTransports` (one changed line in `builder.go`), which records each transport that provider returns in an unexported set of `internal/provimux/origin`; and `http.DefaultTransport` must still be the value it was when that package initialised. A per-credential transport any other provider returns (an embedding's `SetRoundTripperProvider`), a replaced default transport, and a decorator of another program on the context all give none. Removed: recognition by Go package, and `ProvimuxStockTransport` (a transport declaring itself pass-through); a source test fails if either comes back. **Tests** reach the same recognition through `origin.StandInForTests`, which registers a stand-in vendor's transport only in a test binary (`testing.Testing()`); outside one it does nothing (checked by building a program that calls it: the transport is not recognised). The package is internal, so no program outside this module can call it at all, and `TestProvimuxOriginCallers` fails when a product file other than the named ones calls any of the marks or seams.
3. **The credential was made by the gateway's own loader.** An unexported field of `Auth`, set only through `origin.MarkLoaded` by the gateway's own parsers: the auth-file synthesizer for a login it parses natively (never one a plugin's parser claimed), the configuration's synthesizer for its keys, and the four token stores where they read a record (file, git, object, Postgres). `Clone` keeps it, so it travels through the manager, a refresh and an update made from a clone. A credential held in memory by an SDK registration has none, whatever its `FileName`, `source`, `path` or `runtime_only` say (finding 37). Tests mark a login they register in memory through `origin.MarkLoadedForTests` (test binary only).
4. **Exactly one request that could have produced the answer, and the model read from the bytes handed to the transport.** The decorator hands the transport a body that copies what the transport reads, as it reads it (and the same for each body a retry asks `GetBody` for); the model is read from those bytes when the answer is stated, not from a replay. A body the transport did not read to its end, two bodies of one request that differ, and a larger one than 64 MiB are not read: no entry (finding 34). A request on another context than the attempt's is not the attempt's and names none.
5. **Housekeeping is matched by the executor that answered, the exact authority and the exact path together, with its shape**: for Vertex, Antigravity and Gemini a form POST to `oauth2.googleapis.com/token` or `www.googleapis.com/oauth2/v4/token`; for Antigravity JSON POSTs to `/v1internal:loadCodeAssist`, `:onboardUser`, `:fetchUserInfo` on its three hosts; for Copilot a GET of `api.github.com/copilot_internal/v2/token` and a GET of `/models` on its four API hosts. https on the default port, a `Host` equal to the URL's, no model, nothing else doubtful. Another executor's catalog, a lookalike path, a host given by suffix and a token-shaped request with a `Host` of its own are candidates (finding 35).
6. As before: no upstream WebSocket executor names an entry (a connection the decorator did not see cancels); a `Host` that differs from the URL's names none.

**Written limits.**
- *A login a plugin wrote with an earlier version* (finding 36). The earlier plugin conversion (`pluginAuthDataToCoreAuth`) and file writer (`mergedStorageJSON` through the file store) wrote the plugin's own fields, `type` and `disabled`, sorted, as a native login's metadata is written: nothing in the bytes says a plugin made it, so it cannot be recognised by its content. Where the plugin no longer claims the file, the gateway's loader parses it as a native login of its type and its answers name the entry of what the built-in executor sent to the vendor's own host, read from the wire. Native logins keep their entries on upgrade. `TestFollowLegacyCredential` pins this.
- *Code in the same process that changes a Go transport in place* (`RegisterProtocol` on `http.DefaultTransport` or on a transport the gateway built, which any package can reach): the object is still the one recognised. Recognition is of which object, not of what other code in the process did to it. Network-level redirection (DNS, trust roots, a proxy the configuration names) is the operator's.

**On the plugin mark** (`provimux_plugin_credential` in a login's metadata, and the plugin host's storage). With rules 1 to 3 it is no longer what keeps a plugin's credential from an entry in most cases: a plugin's conversion never carries the loader's mark, and a plugin's executor is not on the list. It is still needed for the entry in one case: a login file a plugin wrote with this version, which the gateway's own loader later parses natively (the plugin is gone or no longer claims it). The loader really parsed it and a built-in executor really sends it, so only the mark in the file says a plugin made it. It also still decides the account marker's basis (`s.` for a credential a plugin made). It is kept; removing it is the owner's decision.

**B. The record of past answers: a best-effort memory.** It is a memory of what answers named, rebuilt by the next answer; nothing a user owns, and nothing more is promised than this:

1. **Exactly what the stock output route wrote, read at the moment it wrote it.** `internal/api.NewServer`, for a server built with none of the SDK's options that put another program's code where a response or a WebSocket connection is written (middleware, an engine or a router configurator; the gateway as shipped passes none), marks every request it serves through its base context, with a key no other package can make, and holds, directly over the writer net/http hands it, a writer that reads `X-Provimux-Account` and `X-Provimux-Entry` at the moment the headers are committed (`internal/logging/provimux_stock.go`, one line in `server.go`). The record keeps the account and that entry, the account and `null` where no entry went out, or nothing where no statement went out or what went out is not the statement's. A WebSocket turn is recorded only on a connection the stock route accepted. The gateway's handler served by any other server, with a writer of its own, records nothing (finding 38). Tests serve through the server `NewServer` built, by `ProvimuxStockServerForTests` (test binary only).
2. **Per caller as the gateway authenticates it.** With keys of the configuration that is per key. Where an access provider of an embedding maps several keys to one subject, they are one caller and share one record (finding 39): as intended, and the contract says so.
3. **The newest answer is always kept and always first.** The order is the order the record accepted the notes in, a sequence of its own taken under its lock, never the clock: five answers on one clock tick keep the last four, newest first (finding 40). The file keeps each item's sequence.
4. **The file.** One gateway owns `provimux-answered.v1` in the auth folder, by a lock on `provimux-answered.v1.lock` held while it runs. A second gateway on the same folder keeps its record in memory only, neither reads nor writes the file, and says so once in its log (finding 42). The owner writes over the file only when it is recognisably the record's own (its mark `"format": "provimux answered-by record"`, version 1, and nothing it does not write, read strictly) and unchanged since the owner last read or wrote it (a hash, checked right before the rename). Anything else at that name, at the start or later (another program's file, a damaged or truncated one, one that changed underneath, one written by the earlier build without the mark), is left as it is; the record stays in memory and the log says so once (finding 43). The stop is final: the intake closes, the last write runs, the lock is given up, and nothing opens the intake again in this process; a change of folder that the stop overtakes is not made (finding 41, second half). A note made around a change of folder may be missed (finding 41, first half): as intended, and the documentation says so. With `models.answered-by` off, and in Home mode, the record has no place and the file is not read (the review's note).

| Finding | Answered by | The test (the reviewer's, here; before at `d21428393` → now) |
|---|---|---|
| 32 (an answering channel neither check sees: a pre-opened socket, an untraced client) | A.1 | `TestFollowAnsweringChannel/preopened-socket`, `/separate-untraced`: FAIL → PASS |
| 33 (a self-declaring transport; a Go transport with a registered HTTPS handler) | A.2 | `TestFollowTransportIdentity/self-attested`, `/standard-protocol`: FAIL → PASS (control ported to the test seam) |
| 34 (the replay body differs from the bytes sent) | A.4 (and A.1) | `TestFollowAnsweringChannel/body-readback`: FAIL → PASS; `TestProvimuxSentModelPerWireForm` reads the bytes sent |
| 35 (another executor's catalog, a lookalike path, a token-shaped request to another host) | A.5 (and A.1) | `TestFollowAnsweringChannel/other-executors-catalog`, `/lookalike-discovery`, `/overridden-housekeeping-authority`: FAIL → PASS; `TestProvimuxSentHousekeeping` |
| 36 (an earlier plugin login turned native on upgrade) | written limit (above) | `TestFollowLegacyCredential`, ported to pin the limit |
| 37 (memory-only credentials with a file name or a source string) | A.3 | `TestFollowPositiveOrigin`: FAIL → PASS |
| 38 (an embedding's writer dropped the entry, or the statement) | B.1 | `TestFollowCommittedFields` (ported: nothing recorded), `TestFollowCommittedNoStatement`: FAIL → PASS; `TestProvimuxAnswerRecordsWhatTheStockRouteCommitted` |
| 39 (two keys of one subject share a record) | B.2, as intended | `TestFollowTwoKeysSamePrincipal`, ported to pin it |
| 40 (five answers on one tick lost the newest) | B.3 | `TestFollowEqualClock`: FAIL → PASS; `TestFollowClockInterleave` |
| 41 (a note lost around a move; a stop reopened by a move) | B.4: the first half as intended, the second closed | `TestFollowReloadDuringNote`, `TestFollowReloadWithCommittedHTTP` (ported to pin it); `TestFollowOverlappingCloseAndMove`: FAIL → PASS |
| 42 (two gateways on one folder, one's record gone after a restart) | B.4 | `TestFollowSharedFolder`, `TestFollowSharedServiceFolder` (ported to the decided behaviour) |
| 43 (a foreign or partly written file written over) | B.4 | `TestFollowForeignAndPartialFile`: FAIL → PASS; `TestProvimuxAnsweredChangedUnderneath` |

**What the next step (F3, `include=figures` on `provimux-figures-f3`) must change.** The figures a list sends are those of the entries its rows' `answered` name; those items now exist only where an answer was written by the stock route of a gateway that owns its record's file, so a tool must still read the list again when an answer names an entry the copy it holds lacks. Rebased on this, F3 meets: `ProvimuxSent(ctx, executor)` takes the answering executor's kind; `SetProvimuxAnswerEntry` takes two functions and holds the first call; `ProvimuxNoteWritten` takes the request; the record's file has a mark, a version and per-item sequences, and its earlier form is left as it is; tests that need an entry register their stand-in through `origin.StandInForTests` (or `reviewSetVendor`), their logins through `sentHarness.register` (which marks them) and serve through `ProvimuxStockServerForTests` (as `newSentHarness` does). F3's own tests that set a `RoundTripperProvider` directly, or register credentials with the manager directly, will see no entry and must use those.

**Upstream lines.** Against W3's `516b99486`, in files of upstream v8.1.7: 30 added and 1 changed in 18 files (was 19 in 11). New: `internal/api/server.go` +1, `internal/watcher/synthesizer/config.go` +1, `file.go` +1, `sdk/auth/filestore.go` +1 (now +2), `internal/store/gitstore.go`, `objectstore.go`, `postgresstore.go` +1 each (now +2), `sdk/cliproxy/auth/conductor_execution.go` +1, `sdk/cliproxy/auth/types.go` +1, `sdk/cliproxy/service_executors.go` +1, `sdk/cliproxy/builder.go` 1 changed. None in a protected path.

### (3) Decided by the lead on 2026-11-27, after the check of `d1e4853b2` (findings 44 to 49)

The check found the three marks of A set only where this section says, and three more ways an embedding mixed a foreign part with marked ones (45: an SDK executor credited after two reloads; 46: a decorator of its own around the real transport; 47: a clone of a loaded credential under another id). Rather than one more detection each:

**The process mark.** The entry and the record exist only in a process that Provimux's own `main` started. `cmd/server/main.go` calls `logging.ProvimuxShippedGateway()` as the first thing `main` does (one line); it sets one mark for the whole process in `internal/provimux/origin`. Both packages are internal: a program in another Go module cannot import them, so no embedding can set the mark, and `TestProvimuxOriginCallers` fails when any product file but those two calls the setter. Where the mark is not set: no answer names an entry, nor `X-Provimux-Model-Sent`, `-Model` or `-Model-Basis` (A's own condition); nothing is noted; the record has no place, so its file is neither read nor written; and neither list carries `answered`. The account marker (`X-Provimux-Account`, `account` on the rows) does not depend on it. Tests that stand for the gateway as shipped set the mark through `origin.ShippedGatewayForTests`, which does nothing outside a test binary; a test that stands for an embedding does not (`asEmbedding`) and expects none of it. The three marks of A stay: inside the shipped gateway they keep a plugin's parts out.

**Inside the gateway as shipped.**
- *45.* (Narrowed by part (4) below, replaced by part (5).) The answer is credited to the registration that was in force for the whole call: the call's first attempt context counts the manager's executor registrations and unregistrations (all providers), and an entry is named only if the count at publication is the same. Any registration while the call runs (a reload, a plugin's executor) gives that answer none. This is earlier than the brief's "when the request is handed to the transport": in the reviewer's case the request is handed over after both re-registrations, so a revision noted then would have matched.
- *47.* The loader's mark holds the credential's id when the loader made it, and holds only for that id: a copy registered under another id has none.
- *46.* The gateway as shipped installs no HTTP decorator on a context but Provimux's own (`provimuxObserveSent`); Antigravity's client removes the decorator for an inner client and applies it once itself. A decorator of another program exists only in an embedding, which the process mark excludes.

**The record.**
- *44.* A row or an item without every field the record writes makes the whole file not the record's own: not loaded, not shown, never written over, a warning in the log. Every field is required (`credential`, `marker`, `name`, `client` (which may be the empty string, the keyless caller, but must be there), `items`; and per item `entry` (a string or null), `at`, `seq`), and any field the record does not write, at any level, is refused too. A later version is told by its `version` and is not read by this one either way, so refusing unknown fields costs no later reading.
- *48.* An item's day and time are those of the last answer that named it by sequence, as the clock gave them for that answer, also when the clock was set back in between.
- *49.* Switching to off or to Home mode (or a process without the mark) drops what is pending and empties memory without reading or writing the file.

**The copy of the request body.** A body is copied only for a call whose caller asked for the statement (the handlers say so on the call's context where they install the statement's callback; no other call's answer is stated or recorded, since both need that callback), and the copy is let go as soon as ProvimuxSent has read the models. The JSON reader reads a view of the copy rather than a second copy of it. A body larger than the 64 MiB the reader reads is not copied at all. Reading the model as the bytes pass, keeping nothing, would need an incremental reader for each form (JSON top-level keys with escapes, duplicates and whole-document validity; a zstd stream; Devin's Connect frame; a form), proven equal to the present reader by a differential test over every body shape the built-in executors send and a fuzzed corpus; not built.

**The three model headers.** A (above) says: "An answer names an entry (and `X-Provimux-Model-Sent`, `-Model`, `-Model-Basis`) only when all of this holds". The code now matches it: where a rule of A fails (the credential included), none of the four is sent; where all hold and only the map has no row for what was sent, the three model headers are sent and the entry is not (section 4).

| Finding | Mend | Test (the reviewer's; before at `d1e4853b2` → now) |
|---|---|---|
| 44 | the strict reading above | `TestFinalIncompleteRecord`: FAIL → PASS |
| 45 | the registration count; the process mark | `TestFinalInFlightWatcherReload`, `TestFinalInFlightExecutor` (shipped and embedding): FAIL → PASS |
| 46 | the process mark | `TestFinalMetricsDecorator`, ported to an embedding: FAIL → PASS |
| 47 | the mark per id; the process mark | `TestFinalCredentialCopy` (shipped and embedding): FAIL → PASS |
| 48 | the time by sequence | `TestFinalLastWrittenTime`: FAIL → PASS |
| 49 | drop on disable | `TestFinalDisablePendingWrite`: FAIL → PASS |

### (4) Decided by the lead on 2026-11-27: rule A.1 narrowed to what changes the executor in place

(Replaced by part (5): its table of everyday events holds, its rule does not.)

Part (3) credited an answer only if the manager's count of executor registrations, across all providers, was the same at publication as at the call's first attempt. In the gateway as shipped, everyday events move that count: the service puts its own executor back for a provider whenever a login of that provider is added, refreshed, disabled or removed, at every configuration save (for nine providers at once), and at every model-list refresh; a plugin registers executors for its own providers. With a streaming answer held before it was stated, each of these cost the answer its entry (`TestProvimuxEverydayEventsKeepTheEntry`, below). Narrowed, as the rule now is:

**An answer names an entry only if the executor the manager publishes for the answering credential's provider is the service's own built-in one it recorded, at that registration, and no registration for that provider put another kind of executor in place after the call's first attempt.** The service putting its own built-in executor of the same kind back, with nothing between, is no change. A change is: another program's registration for the provider (seen as a gap in the provider's revisions at the service's next registration, or as a revision that is not the service's at publication), an unregistration, or the service's own registration of another kind (a type not on the list, the plugin-refresh wrapper). Each change is numbered in the order it is seen; the call notes the last number at its first attempt and must see no later change for its provider. Registrations for other providers never matter.

| Event, with a streaming answer held before it is stated | Moves the count of part (3) | Entry before (part 3) | Entry now |
|---|---|---|---|
| nothing (control) | no | kept | kept |
| the answering login's token refreshed, its file saved | yes (claude) | lost | kept |
| a login of the same provider added | yes (claude) | lost | kept |
| a login of the same provider disabled | yes (claude) | lost | kept |
| a login of the same provider removed | yes (claude) | lost | kept |
| a login of another provider added | yes (meta) | lost | kept |
| a login of another provider disabled | no | kept | kept |
| a login of another provider removed | no | kept | kept |
| the configuration saved, an unrelated change | yes (nine providers) | lost | kept |
| the configuration saved, a change to the answering provider's keys | yes (nine providers) | lost | kept |
| the configuration saved, a change to another provider's keys | yes (nine providers) | lost | kept |
| a plugin loaded, for a provider of its own | yes (its provider) | lost | kept |
| a plugin reloaded, for a provider of its own | yes (its provider) | lost | kept |
| the model lists refreshed | yes (seven providers) | lost | kept |
| control: a plugin's executor put in place for the answering provider, then the service's again | yes (claude) | lost | lost, as it must |

Finding 45's tests (`TestFinalInFlightWatcherReload`, `TestFinalInFlightExecutor`, shipped and embedding) still pass: there another program's executor was in place when the call began, and the service's registrations during the call follow a gap.

### (5) Decided by the lead on 2026-11-27, with the owner's word: one standing rule per provider (findings 50 to 55)

The check of `494df49e3` found two more ways the per-call watching of parts (3) and (4) went wrong. **52**: the gateway re-registers its own built-in executors on every configuration save, login change and model-list refresh; when that fell at the moment an answer was published, an ordinary answer lost its entry (62 of 300 streams beside concurrent registrations; 25 of 25 at the point between the manager publishing the executor and the service recording its revision). **53**: a plugin-refresh executor put in place for the answering provider and the built-in one restored while a first attempt ran; the attempt got a 429, the call failed over to a second credential, which took a fresh note, and the answer named an entry it must not. Each of three reviews had found a new way for the watching to go wrong. It is removed. The rule now:

**Once an executor that is not one of Provimux's built-in ones has been put in place for a provider (a plugin's executor, the plugin-refresh wrapper, anything registered by other code), that provider names no entry until the gateway restarts. A provider for which only built-in executors were ever registered names its entries as before.**

- **Where it is set.** In `Manager.RegisterExecutorWithRevision` (`sdk/cliproxy/auth/conductor_lifecycle.go`), upstream's function that every executor registration passes (`RegisterExecutor` calls it; the service, the plugin host and any other code call one of the two; no other code writes the manager's executors). One added line there calls `provimuxRegistering` (`sdk/cliproxy/auth/provimux_answered.go`) after the provider is known and **before** the manager takes its lock and puts the executor in place: the mark is written before the executor can be picked by any call, and a call that picks it up (under the manager's lock) sees the mark at its publication. No layer-only place sees every registration before it takes effect: the service's `registerExecutor` sees only the service's own, and the plugin host calls the manager directly.
- **What decides.** Only what is being registered: its type against the closed list of built-in executors (`provimuxOwnExecutor` in `sdk/cliproxy/provimux_builtin_executors.go`: the list of A.1, and AI Studio's, which is the gateway's own and names no entry). Never what was there before, never a count, a revision or a time. An embedding's `ExecutorWrapper` makes every executor a type not on the list.
- **Why the gateway's own re-registration can never set it.** A built-in executor is on the list, so its registration returns before anything is written, at whatever moment and however often it happens. At publication the gateway reads only the executor the manager publishes for the credential (for its kind) and the provider's mark: there is nothing per call to compare, so no interleaving of registrations and publication can matter.
- **The mark.** Per provider (the credential's executor key, its scheduling alias and the published executor's identifier are all checked), set once, never cleared, kept in memory per manager; the gateway as shipped has one manager for its life, so it is per provider for the life of the process. A test binary's many managers do not share it.
- **With the mark set** for a provider: the account marker as ever; none of `X-Provimux-Entry`, `-Model-Sent`, `-Model`, `-Model-Basis`; `null` recorded for its answers; the log says once, per provider: `answered-by: a plugin took part for provider "<provider>" (an executor that is not one of the gateway's own was put in place); its answers name no providers.dev entry until the gateway restarts`. A caller sees the state only as the missing entry. A user without plugins never meets the rule; a user with a plugin for a provider gets no entry for that provider, by design.
- **Removed**: the per-manager record of the service's registrations (executor, revision, kind, change number), the numbering of changes, the note at the call's first attempt, the comparison at publication, and the service's line that fed the record (`sdk/cliproxy/service_executors.go`, now as upstream's). `newUpstreamAttemptContext` keeps its one line: it now says only which manager runs the attempt.

**The record (51).** Go's JSON decoding matches field names without regard to letter case: `Client` passed for `client`, the file was taken for the record's own, shown, and later written over. The reader now reads the file name by name (`provimuxStrictObject`): every field name at every level (document, row, item) must be the one the record writes, written exactly so (no other case, no escape), and once. A name in another case or spelling, a name twice, a field the record does not write, or a value of another type makes the whole file not the record's own: not loaded, not shown, never written over, a warning in the log.

**The form reader (54).** A form's model was a part of the string the whole form was read into, so a seven-byte model kept the body's storage alive after the copy was let go. The form's model and the path's are now copied out (`strings.Clone`), as the JSON reader's already was: nothing of a request body is reachable once its model is read.

**The positive control that failed by chance (55).** `TestProvimuxSentUnseenConnectionCancels` waited, without knowing it, for the transport's last read of the body: Go's transport reads the declared bytes, sends them, and only then makes the read that meets the end, on its writing goroutine, which on a loopback can come after the answer is back (2 or 3 runs in 1,000). A body is now whole when the transport has read every byte the request declares (Go's transport sends no more than the declared length, and fails a request whose body holds more), or, without a declared length, when it met the end. More bytes than declared, or fewer, are not whole. The reader no longer waits for that last read, in the test or on the wire.

**Written limit 50.** A program that gives its own Go module a path inside Provimux's module path (`github.com/router-for-me/CLIProxyAPI/v8/...`) can compile a reference to `logging.ProvimuxShippedGateway` (and to `origin.MarkShippedGateway`, which it calls), the function that sets the process mark, as a fork that edits `main` could: Go's rule for internal packages goes by import path, and such a path is inside. That program then says it is Provimux as shipped. It is the same as a fork, and treated as one: nothing to build.

| Finding | Mend | Test (before at `494df49e3` → now) |
|---|---|---|
| 51 | the reader name by name | `TestProvimuxRecordFieldNamesExact`: 10 of 10 changed spellings (document `Rows`, `FORMAT`, `version` twice; row `Client`, an escaped `client`, `name` with `Name`, `marker` twice; item `Entry`, `AT`, `seq` twice) FAIL → PASS; the genuine file and an unknown field PASS → PASS |
| 52 | the standing rule | `TestProvimuxReregistrationAtPublication` (the manager publishes the gateway's own executor while the answer is held, the state at the review's checkpoint): 4 FAIL → PASS; `TestProvimuxReregistrationsBesideStreams` (the gateway's own executors put back without pause beside 300 streams): 4, 3, 6 of 300 lost by the service's alone, 300 of 300 with the manager's → 0 |
| 53 | the standing rule | `TestProvimuxForeignExecutorAtAnyMoment` (the real plugin host's plugin-refresh executor, and a plugin's executor, put in place before the call, during its first attempt, during its second after a failover; this answer and every later one): 12 FAIL → PASS; the controls (none, another provider) PASS → PASS |
| 54 | the copy of the model | `TestProvimuxSentKeepsNothingOfTheBody/x-www-form-urlencoded`: 33,560,472 bytes held → 256 |
| 55 | a body whole at its declared length | `TestProvimuxSentDeclaredLengthIsWhole`: FAIL → PASS; `TestProvimuxSentUnseenConnectionCancels` alone: 3 of 3,000 failed → 3,000 of 3,000 |
| the everyday events of part (4) | the standing rule | `TestProvimuxEverydayEventsKeepTheEntry`, extended to a later answer of the provider and one of another provider: every event keeps the entry; the control loses it, now for the later answer too, while the other provider keeps its own |

**Upstream lines.** Against W3's `516b99486`, in files of upstream v8.1.7: +34 −4 in 21 files, as before: `sdk/cliproxy/auth/conductor_lifecycle.go` +1 (the call in the registration function), `sdk/cliproxy/service_executors.go` back to upstream's. None in a protected path.

## Amended 2026-11-27: decided by the lead (the entry only when certain; the record per client key)

Review B of W6a and W6b (`provimux-answered-entry` at `2ec324bf2`, findings 18 to 31) found answers that named an entry the vendor did not receive, and a record that told one client key about another's answers. The lead decided how each finding is answered while the architect is away; this section is that decision as built, to be read against the rest. Where it and a later section disagree, this one holds.

**A. The entry on an answer: only when it is certain.** One rule replaces the separate cases of 4.3 step 2. An answer names an entry only when all of this holds; in every other case it names none, and one noted earlier in the same attempt is cancelled:

1. In the attempt, **exactly one request that could have produced the answer** was answered with a 2xx, and the reader read it whole. Housekeeping is a **closed list** of requests known by destination and shape (`provimuxHousekeepingList` in `sdk/cliproxy/executor/provimux_sent.go`: Google's OAuth token endpoint, as a form; Antigravity's project discovery, onboarding and user information; Copilot's token exchange and model catalog). Every other request counts: a second one cancels, also when it carries no model, goes to another Devin method, or is the same request again. A refused request (not 2xx) does not count, so a failover, a compression retry and a redirect still name the request that answered. A request made on another context than the attempt's own (a detached one) cancels.
2. **No connection was made in the attempt that the reader did not see.** Each attempt's context carries a connection trace (`net/http/httptrace`, which Go's transport and gorilla's WebSocket dialer both call); a connection asked for by a request the decorator observed is counted as seen. An upstream WebSocket, or a client no helper decorated, leaves an unseen connection: no entry.
3. **What the reader read is what left, to the host it priced.** The `Host` a request is sent with must be its URL's host and port (a configured `Host` header cancels). A transport below the reader that is not one of the gateway's own (Go's, or one of the gateway's own packages) cancels, on the client and as the per-credential transport of an SDK embedding; a transport may declare that it hands requests on unchanged (`ProvimuxStockTransport`), which tests' stand-ins do.
4. **The credential's making is positively known.** An entry is given only for a key of the configuration or a login record one of the gateway's own logins wrote (a file, or a record of the git, object or Postgres store), with no plugin's mark. An unknown or empty origin (an SDK registration held in memory) is not vouched for. A credential a plugin makes carries `provimux_plugin_credential: true` in its metadata from the moment the plugin host makes it (a plugin's login, an update a plugin returns with its models), so it is stored with it and survives every reload. Every native login that writes a credential's record (a save with creation intent, through any of the four token stores) sets it to `false`, the same way for every provider. Nothing else in a login record is touched.

**B. The record of past answers (`answered`): per client key, and simpler.** It replaces 4.4's "per marker and name" and the key-value store of W6b.

1. **Per client key.** A row of `/v1/models?include=accounts` gives what the credential's answers for the name said **to the caller's own key**, by day. Callers without a key share one record. The management `model-routes` gives every key's record together, with the time. One client can no longer watch another's activity through the list. The store keeps a keyed hash of the client key under the markers' key, never the key; the credential's id likewise.
2. **Only what the client was given.** A statement is recorded where it is written to the client, after it is written: once the response's headers that carry it are committed (`internal/logging/cpa_trace.go`), or once a WebSocket turn's `response.created` carrying its marker has been written. A statement only offered, or headers committed without one (a keep-alive first), record nothing. An answer that named the account and no entry is recorded as `null`, which is what the client was given.
3. **Under the name called and the credential that answered, exactly.** No case folding: `Lab/Model` and `lab/model` are two rows. The name called where the credential registers exactly it, else that name without its thinking suffix where it registers exactly that, else nothing. A speech call records under the name the client called (`tts-1`), not the model it was routed to. Each credential keeps its own record, also two credentials of one vendor account; the marker is part of the row, so another account's login in the same file starts empty.
4. **Newest first is true**: the time is taken under the lock that orders the list.
5. **The place.** One file of its own in the auth folder (`provimux-answered.v1`, not a login file: the watcher reads `.json` only), rewritten whole and atomically (a temporary file, then a rename) by one writer: when an item is added, dropped or changes its day, when the auth folder changes, and at the stop. What is in memory belongs to one file: when the folder changes, memory is written to the old file and replaced by what the new one holds, never written over it. At the stop the intake closes first, then the last write runs: a note that arrives after that is not accepted. A file that exists and cannot be read is left as it is and not written over; without a usable folder the record lives for the run and the log says so once. An item older than 90 days is not shown, and is removed from the file at the next write; nothing stronger is promised. The file was chosen over the key-value store because it needs no store of its own, no key scan, and no tie to the store's life: the two places the service applies a config and stops already call it.

| Finding | Answered by |
|---|---|
| 18 (an unseen answering request; a detached one) | A.2 and A.1 |
| 19 (a plugin login turning native; a native login keeping a plugin's mark) | A.4 |
| 20 (an in-memory credential priced) | A.4 |
| 21 (a `Host` override; a translating transport below the reader) | A.3 |
| 22 (a second answered request without a model, or to another Devin method) | A.1 |
| 23 (history from an answer whose headers left without a statement) | B.2 |
| 24 (a hidden name's history under a case twin) | B.3 |
| 25 (a speech answer under another name) | B.3 |
| 26 (two credentials of one account sharing a record) | B.3 |
| 27 (newest first false under concurrency) | B.4 |
| 28 (a store move erasing the destination's record) | B.5 |
| 29 (a note lost at the stop) | B.5 |
| 30 (one key watching another's activity) | B.1 |
| 31 (expired data retained in the store) | B.5 |

## Amended 2026-11-26, late evening: the entry is read from the request that was sent

The second review of F1 (the build step that gave each account row a predicted providers.dev entry) found five more rows whose answer was sent as something else, after the first review's six. By the staff's rule the work changes instead of going back a third time. This amendment is that change. It touches **nothing W1 to W3 build**: the marker, its key, `account` in the lists, the statement and where it is written are as they were, and as `provimux-answered-by` has them at `9b71b947f`.

| What changed | Where | Why |
|---|---|---|
| The entry of an answer is worked out from the request the gateway sent: the host and the model are read from that request as the transport was handed it, through upstream's own seam `sdk/httptransport`. The same reading gives `X-Provimux-Model-Sent` | 3.1, 4 (rewritten), 8 | A prediction has to copy every late decision of every executor, and each review found one it had missed. The request has them all in it |
| A list row carries no `entry` and no `entry_per_request`. It carries `answered`: what the account's answers for the name said | 4.4, 4.5, 7.1; `openapi.yaml`; `pui13/openapi.yaml` | A row cannot promise what a request decides |
| The usage reporter is no longer where the sent model comes from. It still gives the model the vendor named, and the effort | 3.1, 3.3, 8 | It is told a model before the payload rules on four executors and never corrected on three more (4.2) |
| W6 needs no owner patch and comes before W4 and W5. It splits in two: the entry on the answer, then `answered` in the lists. A new, optional step W6s covers the upstream WebSocket | 9 | W6 took the sent model from W5 |
| F1 is not merged; its map and the map's tests are W6's first commit | 8.5, 9 | |
| Three decisions more | 10 | |

Read for it: `main` at `9d0786d95`; F1 at `4f14c2cff`; W1 to W3 at `9b71b947f`; both reviews of F1. Three helper agents, read-only, each returning file and line, read every executor's request path again for one question: where the bytes are final, and who is told what before that.

**Why.** Three pieces of work met the same wall on 2026-11-26: a client of Provimux cannot tell firmly which account answered a request, nor which model.

1. The review of the first build step of the figures (`prices-and-cache.md`, F1), finding 8: two Claude keys on one host, one name set on two different models. Both rows read `claude, apikey, no host`, each with another providers.dev entry, and the three answered-by headers say the same for both.
2. Four reviews of the measuring of a login's cache through Pi and Provimux: two logins of one provider cannot be told apart, a login replaced in its file cannot be seen, the Responses WebSocket names no account, and the answer carries the model the client asked for, so an alias, a pool, a fallback or an effort variant answering cannot be seen.
3. pi-provimux's footer "answered by" reads the same three headers.

**Read from.**

| Project | Commit |
|---|---|
| Provimux | `main` at `bc6f81d73`, on CLIProxyAPI v8.1.7 (released as v8.1.7-pmx.1). Every `file:line` below is in that commit. `main` gained two commits while this was written and the branch sits on them (`9d0786d95`): a fix to a key's model list, and a test that pins the three headers on the wire. Neither changes a line cited here |
| The owner's patches this builds on | `f4fda78ea` "name a credential only where its answer is written" and its followers up to `a74fe07c3` |
| F1 in its fix round | `provimux-figures-f1` at `679b8182e` (not on `main`) |
| pi-provimux | `7affe86` |
| The two reviews of 2026-11-26 | of F1's build (findings 8, 1 and 3), and the fourth of the measuring harness in groundtruth (its section on identity and model evidence at the released gateway) |

**Tags.** **[read]** by me in the code. **[helper]** read by a helper agent of mine that returned file and line; I did not open every line again. **[ran]** a command of mine (`git log`, a script). **[infer]** reasoned, not verified; section 11 names what settles each. I sent no request to a provider, to the owner's gateway or to port 8787, and opened no credential file.

## 1. The design in one page

| Piece | What it is |
|---|---|
| The account marker | `a.` or `s.` and 20 characters: a keyed hash of the account's own identity at its vendor, under a key only this installation holds. The same on every answer of one account, another for every other account, the same after a restart or a reorder, another when another account's login takes a file. It says nothing a reader can use except "the same" and "not the same" |
| The answering model | three facts, each said as what it is: the model id in the request the gateway sent, the model id the vendor's answer named, and the effort the gateway sent. Where the vendor's answer named none, the answer says `not-confirmed` |
| The entry of the answer | the providers.dev entry of this one answer, worked out by the gateway from what it sent: the host the request went to and the model id in it. Never predicted. This is the route from an answer to `figures.entries` |
| Where it is written | as five more values of the statement the owner's patch already makes: confirmed where the first byte of the answer is written, and nowhere else. **No new place writes it** |
| On HTTP and SSE | six response headers, `X-Provimux-*`, beside the three `X-CPA-AUTH-*` |
| On the Responses WebSocket | six fields `provimux_*` on each turn's `response.created`, beside `cpa_auth_*` |
| In the lists | every account row of `GET /v1/models?include=accounts` and of `model-routes` carries the marker as `account`, and `answered`: what that account's answers for that name said of themselves (4.4) |
| The switch | `models.answered-by: false` turns all of it off. On unless set |

Six findings shaped it.

1. **The statement is already one value that travels with the answer** [read]. The conductor states who answered once per call (`publishAnsweredAuthMetadata`, `sdk/cliproxy/auth/conductor_execution.go:3353`), the handlers keep it per call and offer it with the result (`sdk/api/handlers/handlers_answered.go`), each route confirms it before its first byte (`handlers.ConfirmAnswer`), and one function writes or removes all headers of it together (`internal/logging/cpa_trace.go:302-320`). A test lists every place allowed to confirm: 31 routes and 6 relays (`internal/api/server_auth_confirm_sites_test.go:21-56`). So the marker and the model need no rule of their own. They are more fields of that value.
2. **The gateway holds a vendor identity for most accounts and uses none of it** [helper]. A Claude login's file has `account_uuid` and `organization_uuid`, a Codex login's `account_id`, Copilot's the GitHub user id, Devin's `user_id` and `org_id`, xAI's `sub`. `Auth.ID` and the index are made from the file's path or the key (`sdk/cliproxy/auth/types.go:686-772`), so neither changes when another account's login takes the file.
3. **Every answer already carries an account-like value, and it is the wrong one** [read, helper]. `X-CPA-TRACE-ID` is `<time>-<auth index>-<request id>`. The index is 16 hex digits of an unkeyed SHA-256 of the login file's absolute path, or of an API key's base URL and the key itself (`types.go:631-638`, `:699-766`). It is set when a credential is **selected**, so after a failover it can name one that did not answer, it stays on errors, and it survives the replacement of a login. Whoever knows a key, or guesses a file's path, can check the guess against it. It is upstream's header (decision 5).
4. **Only the executor's usage reporter knows which model answered** [read, helper]. Every executor tells one object three things: the model it was given, the id it wrote into the request, and the model the vendor's answer named (`internal/runtime/executor/helps/usage_helpers.go:167`, `:283-318`, `:427-438`). That object publishes a usage record after the answer. Nothing carries any of it back to where the answer is written: not the response, not a stream chunk, not a callback.
5. **Upstream already keeps a per-attempt note for one executor** [read]. Since v8.1.3 an attempt's tracker holds the model Antigravity chose for the caller's effort (`SetUpstreamAttemptModel`, `sdk/cliproxy/executor/context.go:20-25`, `:119-138`; written at `antigravity_executor_request.go:117`). The same tracker decides today whether anybody is named at all (`UpstreamAnswered`, `context.go:102`). It is the place for the reporter's three facts.
6. **The model in the body is not evidence of anything** [helper]. A translator writes the vendor's model where it has one and the model the executor was given where it has none; a forced alias writes the client's name over it (`sdk/cliproxy/auth/response_model_rewriter.go:35-53`); the same format passes through untouched. A client cannot tell which of the three it reads. So the model is said beside the body, and the body is left as it is.

## 2. The account marker

### 2.1 What it must be, and how each point is met

| It must | How |
|---|---|
| be the same for every answer of one account | it is made from the account's identity at its vendor, not from the request, the time or the file |
| differ for any other account | a keyed hash of 100 bits; inside the hash each kind of account has its own tag |
| say nothing about the account | HMAC under a key that is never in the config, in an answer or in a log. Without the key a reader cannot check a guess (an e-mail, a key, an organisation id) against it |
| reveal no count and no order | it is not an index and has no sequence |
| stay the same across restarts and reorders | the key is kept on disk; nothing in the hash depends on a position in the config or on a path, except for the `s.` kinds of 2.3 |
| change when another account's login takes the file | the identity is read from the login as it is now, at every use; nothing is remembered per file |
| be matched against the lists | the same function gives `account` on every row of both lists |

### 2.2 How it is made

```
marker   = basis "." base32( HMAC-SHA-256( key, "provimux account marker v1" 0x00 basis 0x00 identity ) )[first 20 characters]
basis    = "a"  the account as its vendor names it      "s"  this gateway's credential, where the vendor's name is not held
identity = kind tag 0x00 field 0x00 field …             (2.3)
base32   = RFC 4648, lower case, no padding
```

It is always 22 characters, `^[as]\.[a-z2-7]{20}$`, plain ASCII. A change of this rule is `v2` inside the hash: every marker changes at once, and nothing else has to move, because a marker is only ever compared with another from the same gateway.

**The key.** 32 random bytes, made once, kept in the gateway's key-value store (`server.kv-store`, a bbolt file beside the logins) under a key of its own, `provimux:answered:v1:key`. It is read before the first request is served: a local file, never a network call. It is not Savings' secret: that one is read inside Savings' save rounds and can change with the store in use (`internal/provimux/savings/saver.go:213-231`) [read]. Where no store keeps it (the store disabled, a read-only disk, Home mode), the key is random for the run, the markers hold until the restart, and the log says so once. Removing the stored key gives every account a new marker.

### 2.3 Per kind of account

Fields are the stored login's own, trimmed, compared as bytes. [helper] for where each field is stored; the Claude and Codex rows [read].

| Account | Identity inside the hash | Basis | Another account's login in the same file | Two credentials of one account |
|---|---|---|---|---|
| Claude login | `organization_uuid`, `account_uuid` | `a` | changes | the same marker |
| Claude login without an organisation id, or whose account id the gateway made up itself (a setup token, a profile the vendor refused: a version-5 UUID of the file's id, `claude_executor_auth.go:88-122`) | the credential's id | `s` | **does not change** | differ |
| Codex login | `account_id` (the ChatGPT workspace), and the user id of its id token where the token names one | `a` | changes | the same |
| Copilot | the GitHub user id (`account_id`) | `a` | changes | the same |
| Devin | `org_id`, `user_id` | `a` | changes | the same |
| xAI login | `sub` | `a` | changes | the same |
| Vertex service account | the service account's `client_email` | `a` | changes | the same |
| Meta login | the e-mail the vendor's token answer names (`user_email`, written again at every mint, `meta_executor.go:148-171`) | `a` | changes | the same |
| Meta login whose vendor answer named no e-mail | the credential's id | `s` | **does not change** | differ |
| Antigravity | the e-mail of the Google account (the only account field the login holds) | `a` | changes | the same |
| Kimi login | its `device_id`, which the gateway makes anew at each login | `s` | changes | differ |
| An API key, any family | the base URL (scheme and host in lower case, the path without a trailing slash, nothing after it) and the key; without a base URL, the provider's name and the key | `a` | n/a: another key is another account | the same |
| An `openai-compatibility` entry without a key | its base URL | `a` | n/a | the same |
| AI Studio (a browser's connection), any runtime-only credential, a credential a plugin's parser made | the credential's id | `s` | new at each connection or load | differ |
| A plugin's executor, a plugin that was handed the bytes, the Home local executor | no statement at all, as today | | | |

- **`a`** means: equal markers are one account at the vendor, and unequal ones are two. **`s`** means: equal markers are one credential of this gateway. Unequal `s` markers can still be one account, and on the two rows marked in bold an equal marker can hide a replaced login. A tool that must prove "the same account" (a cache measurement) accepts `a` and treats `s` as not proven.
- **Meta has no account id** [helper: every stored field listed, `internal/auth/meta/meta.go:89-104`]. Its e-mail is the vendor's own word at every mint, so it follows the login in the file. An e-mail can be changed by its owner, which would give a new marker: the marker then errs towards "not the same", never towards "the same".
- **Why the base URL and not only the host for a key:** one host can front several accounts by path (a gateway in front of several vendors). Two paths of one account then give two markers, which again errs towards "not the same".
- **One account held twice** (the same login in two files, one key in two config rows) has one marker on two rows. That is true: it is one account. Which row answered does not matter for a price, because the answer names its own entry (section 4).
- **A Claude login whose file holds no ids yet** gets them at its first request: the Claude executor reads the profile then (`claude_executor_auth.go:84-128`). Its marker is `s` until then and `a` after. A tool that meets a marker it has not seen reads the list again.

### 2.4 Per gateway, not per client key

The marker is the same for every client key (decision 3).

| | One marker per gateway | One per client key |
|---|---|---|
| Two key holders comparing notes learn | that their requests met the same account | nothing |
| They knew already | the same rows: the gateway routes every key over the same accounts, and the list is the same for every key (it does not read the caller, `openai_models_accounts.go:44-74`) [read] | |
| A tool with two keys (a probe and an agent in one measurement) | can show both met one account | cannot; the measurement falls back to the owner's word |
| The console, Savings, a log line | one value per account | a second, operator-only marker is needed to say which is which |

### 2.5 What a holder of a client key learns

| Learns | New? |
|---|---|
| which row of the list answered each of his own requests, so how his requests spread over the accounts, and when an account stopped answering | yes. It is the purpose |
| that two rows under different names are one account | yes. The count of accounts under each name was already in the rows |
| that the login of an account was replaced | yes |
| the model id behind a name, the effort sent and the vendor's own id for it | yes, on the answers to his own requests. The list shows it too, as what an account last answered as, for any client's requests (4.4; `prices-and-cache.md`, decision 5) |
| that a name was used on a given day, by somebody, on a given account | yes: the day of an item of `answered`. Not the time, and not by whom (decision 13) |
| the e-mail, key, file, organisation or vendor id of an account; how many accounts serve names he cannot see; any order | no |

The operator reads the same marker beside the login's name in `GET /v8/management/provimux/model-routes`, behind the management key. No answer to a client ever pairs a marker with a name.

## 3. The model that answered

### 3.1 Three facts

| Fact | What it is | Who knows it |
|---|---|---|
| **sent** | the model id in the request the gateway sent to the vendor, read from that request as the transport was handed it (4.2): after the name, the alias, the pool's turn, the executor's own mapping and the operator's payload rules | the gateway. Firm where it read the request. Where it could not (an upstream WebSocket), the id is the one its executor was given, and the answer names no entry |
| **reported** | the model id the vendor's answer named | the vendor. Present only when its answer named one before the answer's first byte left |
| **effort** | the reasoning effort in the outgoing request, as read from its final body | the gateway, where the executor reads it |

The gateway judges nothing between the first two. A vendor that answers `claude-opus-5-5-20261101` to `claude-opus-5-5` reports a dated id, not another model; a vendor that answers another model reports that. Both strings are given and the reader compares.

**Why the fallback or the pool's turn is not said as a reason.** It is said as its result: the model sent. A pooled name answers `sent: a` on one request and `sent: b` on the next. A reason would need a line in each of the conductor's attempt loops, in the file upstream changed most (section 8.3), for a word no tool prices or measures by.

### 3.2 On the wire

| Header | WebSocket field | Value | Absent when |
|---|---|---|---|
| `X-Provimux-Account` | `provimux_account` | the marker | nobody is named (section 5.3), or the switch is off |
| `X-Provimux-Model` | `provimux_model` | the reported id where there is one, else the sent id | the answer came through a relay (section 5.2) |
| `X-Provimux-Model-Basis` | `provimux_model_basis` | `vendor`: the vendor's answer named this model. `not-confirmed`: it named none before the first byte; this is the id the gateway asked for | with `X-Provimux-Model` |
| `X-Provimux-Model-Sent` | `provimux_model_sent` | the sent id | with `X-Provimux-Model` |
| `X-Provimux-Effort` | `provimux_effort` | the effort in the outgoing request | the executor read none from the request |
| `X-Provimux-Entry` | `provimux_entry` | the providers.dev entry of this answer (section 4) | the gateway has no entry for it |

- **Whole or not at all.** The six are written in the one pass that writes the three `X-CPA-AUTH-*`, from the one confirmed statement. An answer has `X-Provimux-Account` or none of the six. Inside a statement, a missing header means what the last column says, and nothing else.
- **Plain ASCII.** A value is sent as the provider is today (`AuthStatement.OnTheWire`, `cpa_trace.go:56-77`): a byte outside printable ASCII, a space or `%` as `%` and two hex digits. Every id a vendor uses today goes out unchanged.
- **The body is not touched.** No field of any body changes. On the WebSocket the fields sit at the top of `response.created`, where the three `cpa_auth_*` already are.
- **`not-confirmed` is said, not implied.** A tool that needs the model proven (a measurement) accepts `vendor` only.

### 3.3 What each executor knows when the first byte leaves

[helper] for the lines; the reporter's own functions [read]. The column **Sent** says what each executor writes; the gateway reads it from the request (4.2), not from the executor's own account of it.

| Executor | Sent | Reported, non-stream | Reported, at a stream's first chunk | Effort |
|---|---|---|---|---|
| Claude (login, key; Kimi over Claude's protocol) | the id after its own mapping | `message.model`, and Anthropic's own fallback model where it used one | yes: `message_start` is the first event | from the request |
| Codex, HTTP and its upstream WebSocket | the final body's `model` | `response.model` | yes: `response.created` | from the request |
| Meta | the id it was given | yes | yes | from the request |
| xAI | the id it was given | yes | yes | from the request |
| Gemini key, Vertex, AI Studio | the id in the URL | `modelVersion` | yes: on every chunk | from the request |
| Antigravity | the variant it chose for the caller's effort | `modelVersion` | yes | from the request |
| Kimi | its mapped id (several names map to one) | yes | yes | from the request |
| `openai-compatibility`, OpenRouter | the id it was given | yes | **no**: the reporter gets the stream's model when the stream ends (`usage_helpers.go:1299-1301`, `:1570`) | from the request |
| Copilot | the bare id | yes | **no**: at the end (`copilot_executor_response.go:276`) | from the request |
| Devin | its own request id, which carries the effort | yes, where the usage frame names one | **no**: the usage frame comes late | not said: the effort is inside the sent id |
| Token counts, every executor | as above | **no**: a count answer names no model | | as above |
| A plugin's executor | no statement | | | |

So `not-confirmed` is the answer today on: a stream from an OpenAI-compatible key, OpenRouter, Copilot or Devin; every token count; and any vendor answer that names no model. Nothing is echoed there: the value is the id the gateway asked for, and the basis says so.

**Two limits, said in the contract.**

- The statement is of the answer's **first byte**. A vendor that switches model later in a stream is in the gateway's usage record and not in the headers. The record is the operator's.
- For Devin a client cannot read the effort as its own value. It is in `X-Provimux-Model-Sent`, the id Devin was asked with.

## 4. The entry of an answer

Rewritten 2026-11-26, late evening (the note before section 1). `X-Provimux-Entry` is `<providers.dev provider>/<model>`. **It is read from the request the gateway sent for this answer, in one place, when the answer is stated. Nothing predicts it**, so no row of a list carries one.

| The answer has | Means | The tool |
|---|---|---|
| `X-Provimux-Entry`, and it is a key of `figures.entries` | this is what the answer cost and how long its cache lives | uses the figures |
| `X-Provimux-Entry`, no such key in the copy the tool holds | the account had not answered for this name when the tool read the list, or providers.dev has no entry for it, or figures are off or not fetched | reads the list once more. Still no key: it has no figures, says so, and guesses nothing from the name |
| a statement without `X-Provimux-Entry` | the map has no row for what was sent, the gateway did not read what it sent, or it does not vouch for the credential (`prices-and-cache.md` 6.1, the three kinds) | has no figures |
| no statement | nobody is named (section 5.3) | the same; and it does not know which account answered |

### 4.1 Why it cannot be predicted

F1 predicted an account's entry from the configuration and the registry. Its two reviews found eleven rows whose answer was sent as something else. Each time the cause was a decision upstream takes inside an executor, after the last thing a list can see [read: the two reviews; the lines below read by me or by a helper that returned file and line]:

| Decided | Where | A list cannot see it because |
|---|---|---|
| The model in the body | the operator's payload rules. They run last, on the finished body, and no rule guards the path `model` (`helps/payload_helpers.go:274-414`). Upstream's own rule for every executor is that they are "the final semantic barrier before sending" (`AGENTS.md`) | a rule can match on a request header. The same account and name send two models (finding 15) |
| The host | the account's base URL; a base URL in a login's file or metadata (`meta_executor.go:320-365`, `devin_executor.go:3157-3194`, `antigravity_executor_request.go:889-954`); xAI's `using_api` (`xai_executor_request.go:279-344`); the Codex second sign-in by the client's `User-Agent` (`codex_executor_auth.go:529-551`); Vertex's route by the alias asked for (`helps/vertex_openapi.go:112-173`) | half of these are per request, and one account can send to two hosts (findings 3, 13, 17) |
| The variant | Antigravity's stored catalog at the moment of the request (`antigravity_model_enums.go:191-276`) | the catalog changes between the list and the call, and a failed read looks like "no group" (finding 16) |
| Who owns the account | a plugin may change an account's provider in the call that publishes its models (`service_executors.go:699-779`), and nothing records that it did | finding 14 |

A prediction must copy every one of these and is wrong where it misses one. The request that was sent has all of them in it already.

### 4.2 The facts, and where each is taken

| Fact | Taken from | It is the sent one because |
|---|---|---|
| **The host** | the URL of the `*http.Request` the executor's HTTP client hands to its transport | every choice of a destination has been made: a redirect the client follows is one more request and is seen too |
| **The model** | the same request, as its bytes are, by where its protocol puts the model: the top-level `model` of a JSON body; the id in a URL path of the form `…/models/<id>:<method>` (Gemini and Vertex); field 21 of the Connect message (Devin, `helps/devin_wire.go`). Where a request names one in its URL and one in its body they must be the same, or nothing is read | the payload rules have run, and so has every mapping of the executor. The body is the one the vendor gets |
| **What signed it**, where one host serves two kinds of access | the names of the request's credential headers (for Claude on `api.anthropic.com`: `x-api-key`, or `Authorization` without it). Never a value | a login's token configured as a key is sent as a login's token (`claude_executor_request.go:1231-1251`) |
| The executor's provider, the credential's kind | the credential the conductor states (`auth.Provider`, `auth.AuthKind()`), as today | they are not on the wire. They choose a row of the map and are never enough for one |

**The place is upstream's own.** `sdk/httptransport` (commit `51952a7b2`, 2026-10-13, unchanged since) is "an additive, request-scoped observation seam": a decorator on the execution context wraps the transport a client has selected, "to observe final HTTP requests". Every HTTP client an executor sends a model request with is built by a helper that applies it after the proxy, the pool and the TLS profile are chosen [helper, each executor's client read]:

| Client | Built by | Decorated |
|---|---|---|
| Claude; Codex over HTTP; Kimi's Claude route | `helps.NewUtlsHTTPClient` (`utls_client.go:483-499`) | yes |
| Copilot, OpenAI-compatible and OpenRouter, Kimi, xAI over HTTP, Meta, Gemini, Vertex (both routes, and its token exchange), Codex images | `helps.NewProxyAwareHTTPClient` (`proxy_helpers.go:36-37`) | yes |
| Devin | `helps.NewDevinHTTPClient` (`proxy_helpers.go:516-517`) | yes |
| Antigravity | `newAntigravityHTTPClient` (`antigravity_executor.go:514-550`), which decorates once itself | yes |
| Codex and xAI over the upstream WebSocket (`websockets` on, and the client came over a WebSocket) | a `websocket.Dialer`; no `http.RoundTripper` sees the handshake or a frame (`codex_websockets_connection.go:233-293`, `xai_websockets_executor.go:1386-1409`) | **no** |
| AI Studio | a JSON message on a socket the browser opened (`internal/wsrelay`) | **no** (and it has no row in the map) |

Nothing in the gateway attaches a decorator today [ran: `git grep`; only tests do]. Provimux attaches one, in one place: where an attempt gets its tracker (`WithUpstreamAttemptTracker`, `sdk/cliproxy/executor/context.go:68`). It is stateless. For each request it is handed it finds the attempt's tracker in the request's own context and notes there, as one **observation**: the scheme, host and port of the URL, the model it read, the credential header names, and, when the response's headers arrive, the status. It delegates to the transport below it and changes nothing: the body is read through `GetBody` (a second reader over the same bytes), never consumed. A decorator an embedder of the SDK put on the context stays, inside ours.

**What is not read, and why** [read, helper]:

| Source | Why not |
|---|---|
| The model the usage reporter was told (`SetUpstreamModel`, the model it was made with) | told **before** the payload rules on Claude (`claude_executor_execute.go:45` against `:254`), Copilot (the route's id, `copilot_executor.go:297`, rules at `:263`), Kimi (`:151` against `:170`), Devin (`:256`, the id from before the rules), and never corrected on the OpenAI-compatible, xAI and Meta executors. Only Codex reads it from the final body. This is finding 15, and it is not only Copilot's |
| The request upstream logs (`RecordAPIRequest`) | it does nothing in commercial mode; Antigravity gives it no body unless request logging is on (`antigravity_executor_request.go:228-258`); Codex gives it the plain JSON while the wire is zstd, and nothing when it manages the instructions; it has the URL before a redirect |
| The account's base URL (`X-CPA-AUTH-HOST`) | empty for every login, and the configured host for a key even when the request went elsewhere |
| The registry, a listing, the name asked for | F1 |

### 4.3 The rule

(Amended 2026-11-27 by the lead: step 2 is now rule A of the amendment at the top: exactly one candidate request, no unseen connection, what was read is what left, and a credential of positively known making.)

Where the conductor states who answered (`publishAnsweredAuthMetadata`, then `publishProvimuxAnswer`, which W3 built), with the attempt's context and the credential that answered:

1. The attempt answered and its bytes are not a plugin's (`UpstreamAnswered`, as today). Else nobody is named.
2. Take the attempt's observations that carried a model and whose response was a `2xx`. If they agree in where they went (scheme, host and port), in model and in signer, that is **what was sent**. If there is none, or they differ, nothing is known: no entry.
3. The gateway vouches for the credential: it is not held in memory only, and no plugin made or changed it (its storage is not the plugin host's: `pluginAuthDataToCoreAuth`, `internal/pluginhost/auth_provider.go:1334-1396`). Else no entry.
4. The map (`prices-and-cache.md` 6.1) has a row for the executor's provider, the credential's kind or signer, and the host that was sent. Else no entry.
5. The entry is that row's provider and the model that was sent.

`X-Provimux-Model-Sent` is the model of step 2 (section 3.1).

Why step 2 is enough, case by case [helper for what each executor sends beside its model request]:

| An attempt also sends | Seen as | Outcome |
|---|---|---|
| A token refresh, a key mint, a service account's token exchange, project discovery, a credits read, a grounding `HEAD` | no model in it. And a refresh after a `401` runs under a tracker of its own (`conductor_execution.go:1423`, `conductor_stream.go:793`), and the Claude, Codex, Meta and xAI refreshes use clients no decorator wraps | ignored |
| A retry after such a refresh | the conductor gives it a new attempt context and states the answer with that one (`conductor_execution.go:1430-1434`, `:1503`) [read] | the retry's own request |
| The same request again: after a `400` to a zstd body, after a `401`, a compaction that re-enters `Execute` with the same model | the failed one is not a `2xx`; the second agrees | the entry |
| A redirect the client follows | the first response is a `3xx`; the second request names the host that answered | that host's row, or none |
| A second model request with another model or host | two observations that differ | no entry |
| A model request over a channel the decorator does not see | no observation | no entry |
| A `2xx` body the executor then treats as an error | the call returns an error | nobody is named |

**What the transport cannot see** is the one gap, and it fails closed: an answer that came over an upstream WebSocket has a statement and no entry. Closing it is step W6s (section 9, decision 12): one line where a message is handed to the socket, beside the `MarkUpstreamAttempt` that is already there, in four files under a protected path. It is the owner's, and nothing else waits for it.

**The cost.** One more read of each outgoing body to find `model`: a scan of the bytes already in memory. A zstd body (Codex, `upstream-request-compression`, off unless set) is decoded for it. With `models.answered-by: false` the decorator is not attached and nothing is read.

### 4.4 What a list row carries: `answered`

(Amended 2026-11-27 by the lead: the record is per client key, per credential and per exact name, recorded where the answer is written, and kept in a file of its own: rule B of the amendment at the top. Where this section says "any client's", "per marker and name", "in the key-value store" or "before the first byte", rule B holds.)

A row cannot say what the next request will be sent as. It can say what this account's answers for this name were sent as. So in place of `entry` and `entry_per_request` a row carries a record:

```json
{ "provider": "codex", "kind": "oauth", "priority": 0, "account": "a.tytzm54pttsi5uuwvreh",
  "answered": [ { "entry": "openai-codex/gpt-6.1-sol", "on": "2026-11-26" },
                { "entry": null,                       "on": "2026-11-19" } ] }
```

| | |
|---|---|
| What an item is | one distinct outcome of step 5 on this account's answers for this name: an entry, or `null` for answers that named none. With the day (UTC) of the last answer that had it. In `model-routes` the same items carry the time (`at`), behind the management key |
| Order and size | newest first; at most four. A fifth distinct outcome drops the oldest |
| When it is written | in `publishProvimuxAnswer`, with the statement: so before the first byte of that answer leaves. A tool that reads the list after an answer finds that answer's entry on the row |
| Under which name | the model the client asked for, where this account registers exactly that id; else that id without its thinking suffix, where the account registers that. Else it is not recorded: there is no row it could stand on. An alias, a prefix and a pooled name are the ids the account registers, so they are their own rows |
| Whose answers | any client's. The record is per gateway, as the marker is (2.4) |
| How long | 90 days from the last answer (providers.dev's own threshold for "old"). Kept in the gateway's key-value store beside the marker's key, under the marker and the name; written when an item is added, when its day changes and when the gateway stops, so at most once a day per item while it runs. After a crash an item's time can be hours old; its day is right. Where no store keeps the key, the record lives for the run |
| Another account's login in the same file | has another marker, so it starts with nothing |
| The switch | with `models.answered-by: false` no row has it and nothing is recorded |
| What it never is | a promise. The next answer is read from its own request. The contract says so on the field, and the console and Pi say "last answered as" |

**Why a day in the client list and a time behind the management key.** The record is of every client's answers. A time to the second would tell one key holder when another was at work. A day tells him that the name was used that day (decision 13).

### 4.5 What each reader has before a first answer

Three ways were weighed: **(a)** no entry at all on a row; **(b)** the record above; **(c)** a predicted entry kept for a closed set of cases where the prediction and the sending provably run the same code.

**(c) is refused.** There is no such set. The payload rules alone reach every executor and may match on a header, so no account's model is fixed ahead of a request while any rule can write `model`; "no rule writes it" is a condition over the configuration, which is what F1 was. The set would be defined by what it excludes, and each review would find one more thing to exclude.

| Reader | Needs before a first answer | With (a) | With (b), chosen |
|---|---|---|---|
| pi-provimux, the model list | a price per model when it registers the models, before any message | models.dev's price for every model, as today. A name the owner set has none there | the figures of the entry the account tried first last answered as, where one is recorded. For an account nobody has used yet: models.dev, as today. It re-reads after an answer whose entry is new |
| pi-provimux, the footer | nothing: it is about an answer | the answer's own entry | the same |
| pi-cache-keeper, the lifetime to rely on | **nothing.** It acts on a cache only once an answer has written or read one (`index.ts:679`), and takes the lifetime of what answered (`index.ts:1016-1017`) | the answer's entry gives it | the same |
| pi-cache-keeper, the window and output limit of a model | a value when the session starts | none from Provimux; the window Provimux registers for the model stands | the last answered entry's, where one is recorded |
| The console, a name's card | the figures of each account, when the page opens. The console sees no answers | **nothing, ever**: F4 has no figure to show | per account: "last answered as …, 2 hours ago" and that entry's figures; "no answer yet" for an account that never answered the name |
| `include=figures` (F3) | which entries' figures to send | none can be chosen; the contract would need another shape (every held entry, about 580, or a request per entry) | the entries the rows name; a tool that meets a new one reads again |
| The measuring tool | nothing from a row but the marker of the login it means to measure. Each sample is proven by its own answer: the marker, the model, the entry | complete | the same, and it can see before it spends a run that the login last answered as something else |
| Savings by account (F6) | nothing: it values requests that were answered | the attempt's entry | the same |

What (b) loses against F1's predicted entry: a price for a model **nobody has asked this account for yet**, and certainty about the next answer, which F1 claimed and did not have. What it cannot do is name a wrong entry for an answer: an item is a copy of what an answer said.

**Why a third review cannot break it by one more configuration.** Whatever is configured, a row says only "an answer of this account for this name was sent as X on that day". That is true if the recording is right, and the recording does not read the configuration. A review can still find a recording fault (a wrong row, a wrong day, an item that no answer carried), and section 8.4 tells it where to look.

### 4.6 The findings of the two reviews against this

[read: the second review's findings 13 to 17 and note 9, each with its capture; the first review's 1 to 3.]

| Finding | Before | Now | By construction? |
|---|---|---|---|
| 13, a Vertex account, one alias native and one over OpenAPI | both rows `google-vertex/gemini-2.5-pro` | the native answer: `google-vertex/gemini-2.5-pro`, the id in its URL. The OpenAPI answer: `google-vertex/google/gemini-2.5-pro`, the `model` of its body. A Vertex key on `openrouter.ai`: `openrouter/acme/provimux-test-base`, as sent | yes: the model is read where each route puts it |
| 14, a plugin publishes a model and sets the account's provider to `meta` | `meta-subscription/…` | no entry: the credential is one a plugin changed (step 3) | by step 3, which reads the credential's storage. It is the one finding the wire alone does not settle: the request really went to `api.meta.ai` |
| 15, a payload rule by header sets another `model` | both requests `github-copilot/provimux-test-native` | each answer names the model in its own body: `…-native`, and `…-alternate` for the request with the header | yes: the body is read after the rules, where the vendor reads it |
| 16, Antigravity: a group discovered between list and call; a failed read of the stored group | the row said `…-low`, the request sent `…-high` | the answer names the variant in its body, whatever the catalog was a moment before, and whether or not its read failed | yes: no catalog is read |
| 17, an xAI key sent to the Grok CLI proxy, or to a relay in its metadata | `xai/grok-4.3` three times | `api.x.ai`: `xai/grok-4.3`. `cli-chat-proxy.grok.com` and the relay: no entry, the map has no such host | yes: the host is the request's |
| Note 9, the mirror's drift test stayed green under a changed alias lookup | both lists quoted the public alias | no mirror and no drift test exist. A changed lookup changes what is sent, and that is what is read | yes |
| First review 1, Copilot's id; a Vertex resource path; Antigravity's variant | the namespaced id | the bare id in the body; the normalised path; the variant sent | yes |
| First review 2, a plugin's model on a native account | `github-copilot/…` | the plugin's executor answered: nobody is named. A native request for that id: `github-copilot/<id>`, which is what it is | yes for the first; the second is decision 14 (`prices-and-cache.md`, decision 18) |
| First review 3, a login whose file points elsewhere; Codex's second sign-in | the subscription's entry | the host that was sent has no row: no entry | yes |

The list rows of all of these: `answered`, which holds what the answers above said.

**What this means for the measuring of a login's cache** (kept from the first writing). Pi is not a Codex client by its `User-Agent`. On a Codex login with the second sign-in bound (`use_siwc`), Pi's requests go to `api.openai.com` with the login's second token and the Codex CLI's to the ChatGPT backend (`IsCodexUseSIWCForRequest`, `sdk/cliproxy/auth/types.go:1085`). A pair of those two compares two backends. The answer says which: no `X-Provimux-Entry` on the one, `openai-codex/…` on the other, each read from where the request went. Whether the owner's login has the binding is in his file, which I did not open.

## 5. Where it is written

### 5.1 The rule

Only where the answer is written. The six are made from fields of the statement the owner's patch carries; they are captured with it, offered with it, withdrawn with it, confirmed with it and written by the same lines. This design adds **no confirming place and no writing place**: the test that lists the 37 places is unchanged, and a route that names nobody today names nobody after.

### 5.2 Per transport and path

| Path | Carries | By |
|---|---|---|
| HTTP JSON: chat completions, completions, Responses and its compact, Claude messages, Gemini `generateContent` and Interactions | all six, where known | `handlers.ConfirmAnswer` at the write, as today |
| SSE streams of the same | all six, on the response's headers, committed with the first chunk of the answer | the same. A stream's reported model is what the first chunk's upstream frames named (3.3) |
| Token counts (`/v1/messages/count_tokens`, Gemini `countTokens`) | all but a reported model: basis `not-confirmed` | the same. A count the gateway worked out itself names nobody, as today |
| Images, videos, speech | the marker; the model where the executor reports one | the same |
| The Responses WebSocket | six fields at the top of each turn's `response.created`. The copies an upstream gateway may have put there are removed first, as for `cpa_auth_*` (`openai_responses_websocket_forward.go:56-87`, `:285-288`) [read] | the turn's sink, which today receives three strings and then receives the whole statement (8.2) |
| The relays that hold their credential themselves: Codex alpha search, the live call routes, the Muse relay (6 places) | the marker only. They run no executor, so there is no model statement and no entry | `Auth.AuthStatement()`, which they already confirm |
| The realtime WebSockets (`/v1/realtime`, the live sideband) | nothing, as today: their frames are relayed raw | out of scope |
| `GET /v1/models`, management answers | no statement: they are not a vendor's answer | |

**Why fields of `response.created` and not a frame, a first message or the upgrade's headers.** A connection is not one account: each turn selects, a failed attempt retries on another credential inside the turn, and the upgrade's `101` is written before any credential is chosen (`openai_responses_websocket.go:459`, `:1022-1070`) [helper, the sink read]. `response.created` is the first event of every answer, the three fields are there already, and pi-provimux reads them there per response id. A frame of our own would be a new event type every client of the socket has to ignore.

### 5.3 Where nobody is named

Unchanged, and now true of nine values and not three. Each is a missing statement, which a tool reads as "not known", never as an account [helper for the lines].

| Case | |
|---|---|
| An error, the gateway's own or a vendor's passed on | every error return offers nothing |
| A keep-alive that left before the answer | it commits the headers; the confirmation after it does nothing (`handlers_answered.go:57`). Only with `streaming.keepalive-seconds` and `send-keepalive-before-ft`, or `nonstream-keepalive-interval`, set: all off by default (`config.example.yaml:333-339`) |
| A plugin's executor, a plugin handed the bytes, the Home local executor (`acp/…`) | the attempt is marked (`MarkPluginBytes`) |
| A response interceptor that changed the body; any stream with a chunk interceptor | the offer stays empty |
| A plugin's own model call | it answers the plugin, not the client |
| A WebSocket turn answered locally (the pre-warm), or with no `response.created` | nothing to put it on |

A measurement that needs every answer named runs with the keep-alives off, or counts an unnamed answer as not proven.

## 6. Who may see it, and the switch

- **Every holder of a client key, on the answers to his own requests and in the list he may read.** There is no narrower audience to give it to: the gateway has no rule per client key, and a holder who can ask can also be answered.
- **Under "serve only my names" nothing differs.** The rule decides which names are served and listed (`only-my-names.md` section 18). An answer is always for a served name, its account is in the list under that name, and the list shows only served names. The model behind his name is shown, on the answer and in the list's `answered` (`prices-and-cache.md`, decision 5): the rule was never a promise to hide it. The switch below hides both, since the list's record is made of the answers' statements; it is the one setting over both.
- **Always on, not asked for per request.** Asking protects nothing: the one who asks is the one who would be refused. Vendor clients send no header of ours, and the tools that need it most (a probe, a footer) would each have to add one to every request.
- **The switch.** `models.answered-by`, beside `live-lists` and `published-lists` (`internal/registry/catalog_config.go:16-28`):

```yaml
models:
  # On unless set to false. Each answer names the account that gave it by an opaque marker, and the
  # model that answered (the X-Provimux-* response headers; on the Responses WebSocket the provimux_*
  # fields of response.created), and the account lists carry the marker. false: none of it. The three
  # X-CPA-AUTH-* headers are not affected.
  # answered-by: false
```

  Off in a test binary unless a test turns it on, as the two list switches are, so that every existing test of the headers reads what it read. With it off, every answer and every WebSocket event is byte for byte what `bc6f81d73` writes, and the lists are what they are without this design.

## 7. What uses it

### 7.1 The figures contract

`prices-and-cache/openapi.yaml`, amended with this document. Contract 1 has not been released, so this is a correction of it, not a contract 2.

| | Was | Is |
|---|---|---|
| From an answer to its figures | match `X-CPA-AUTH-KIND`, `-PROVIDER`, `-HOST` to an account row; take the row's `entry` | read `X-Provimux-Entry` on the answer; it is the key of `figures.entries` |
| "Accounts that agree in all three have the same entry" | promised, and false (finding 8) | removed |
| An account row | `provider`, `kind`, `priority`, `host`, `entry` | `entry` is gone. A row has `account` (the marker) and `answered` (4.4) |
| From an answer to its account | not possible | `X-Provimux-Account` equals the `account` of the row |
| No entry | "an account without `entry` has no figures" | the four cases of section 4 |
| The promises | "an account is found from the three headers" | "an answer names its entry and its account itself; from the moment it leaves, its row's `answered` has that entry; no row says what the next answer will be" |

Finding 8's case, after it: both rows read `claude, apikey`, each with its own `account` and, once it has answered, its own `answered`; the answer carries the marker of the key that answered and `anthropic/provimux-test-dear` or `…-cheap`. Nothing is matched by kind, provider or host.

### 7.2 The others

| Reader | Change |
|---|---|
| Savings by account (`prices-and-cache.md` section 7, F6) | a stored row is keyed by the marker, not by the index. That it holds across a restart and a token refresh is then by construction, and a replaced login starts new rows, which is right. The marker is taken where the row is recorded, from the account the record names. The row's entry is the attempt's (4.3), never the map over the account's configured host |
| A login in overage (`prices-and-cache.md` 4.4, `cache_now`) | the row it would sit on is found by `account` |
| pi-provimux (F5) | the footer shows the answer's own entry and price; its `model-upstreams:answered` event gains `account`, `model`, `model_basis`, `entry`. Before an answer it prices a model by the row's `answered` (4.5). Its two tables that guess a provider from kind, provider and host are no longer needed for a gateway that says it |
| The measuring (groundtruth) | per sample: the same `a.` marker on the prime and on every check, basis `vendor`, the model and effort it expected, and the entry of the route it claims. An `s.` marker, `not-confirmed` or a missing statement makes the sample not proven. `X-CPA-TRACE-ID` is no longer read |
| The console | the marker beside each route, and each route's `answered` with its times: the figures on an account's line are those of the entry it last answered as (`prices-and-cache.md` section 7) |

## 8. The build

### 8.1 New files

| File | Holds | In |
|---|---|---|
| `internal/logging/provimux_answer.go` | the six header names and field names, the type `ProvimuxAnswer`, how it goes on the wire, the function that sets or removes the six headers | layer |
| `sdk/cliproxy/auth/provimux_account_marker.go` | the identity of 2.3 per kind, the rule of 2.2, the key's setter | layer |
| `sdk/cliproxy/auth/provimux_answered.go` | builds the answer's statement at the place the conductor states who answered: the marker (built, W3), what the attempt sent and its entry (W6a), the record of 4.4 (W6b), the attempt's report (W5) | layer |
| `sdk/cliproxy/executor/provimux_sent.go` | what an attempt sent (4.2): the observation, the decorator on `sdk/httptransport`, the readers of a model, the rule's step 2 | layer (W6a) |
| `internal/provimux/modellists/figures_map.go` | the map of `prices-and-cache.md` 6.1, keyed by what was sent. From F1's branch, with its matrix test (8.5) | layer (W6a) |
| `sdk/cliproxy/provimux_answer_entry.go` | steps 3 to 5 of the rule: the credential the gateway vouches for, the map, the entry | layer (W6a) |
| `internal/pluginhost/provimux_plugin_credential.go` | whether a plugin made or changed a credential | layer (W6a) |
| `sdk/cliproxy/provimux_answered_record.go` | the record of 4.4: kept per marker and name, read by both lists | layer (W6b) |
| `sdk/cliproxy/provimux_answered_key.go` | makes, keeps and reads the key; the switch's default | layer |
| `sdk/api/handlers/handlers_provimux_answered.go` | the capture's second setter; the WebSocket turn's sink for a whole statement | layer |
| `sdk/api/handlers/openai/openai_responses_websocket_provimux.go` | removes and sets the six fields of `response.created` | layer |
| `sdk/cliproxy/executor/attempt_report.go` | the attempt's report and the handle the reporter writes through | **owner patch** |
| their `_test.go` files, and `test/provimux/answered_by_test.go` | 8.4 | |

```go
// internal/logging

// ProvimuxAnswer is what Provimux adds to the statement of the credential that produced a response: the account's
// marker, and what the attempt knew of the model when the response was returned. It is part of AuthStatement, so it is
// captured, offered, withdrawn, confirmed and written with it, and never apart from it.
type ProvimuxAnswer struct {
	Account       string // "a." or "s." and 20 characters; "" names nobody and sends nothing
	ModelSent     string // the model id written into the request to the vendor
	ModelReported string // the model id the vendor's answer named before the response was returned; "" = none
	Effort        string // the reasoning effort read from the outgoing request; "" = none read
	Entry         string // the providers.dev entry of this answer; "" = the gateway has none
}

// Fields returns the header name, the WebSocket field name and the value of each of the six, in a fixed order, as
// they are sent: none when Account is empty, and no model field when ModelSent is empty.
func (a ProvimuxAnswer) Fields() []ProvimuxField

// SetProvimuxAnswerHeaders sets, or deletes, all six headers from one statement.
func SetProvimuxAnswerHeaders(header http.Header, statement AuthStatement)

// sdk/cliproxy/auth

// SetProvimuxMarkerKey gives the key of the account markers. Without one no marker is made and nothing is sent.
func SetProvimuxMarkerKey(key []byte)

// ProvimuxAccountMarker returns the account's marker (answered-by.md 2.2 and 2.3), or "" without a key.
func (a *Auth) ProvimuxAccountMarker() string

// provimuxAccountIdentity returns the basis ("a" or "s") and the bytes hashed for a.
func provimuxAccountIdentity(a *Auth) (basis string, identity []byte)

// publishProvimuxAnswer completes the statement the conductor has just made for the response it returns, and hands
// it to cliproxyexecutor.ProvimuxAnswerCallbackMetadataKey. It is called only from publishAnsweredAuthMetadata, after
// its callback, with the same attempt context and credential; for an empty statement it hands over the empty answer.
func publishProvimuxAnswer(attemptCtx context.Context, meta map[string]any, auth *Auth, statement logging.AuthStatement)

// provimuxAnswerEntry is set by sdk/cliproxy to the one function that gives an entry: steps 3 to 5 of answered-by.md
// 4.3, for the credential that answered and what its attempt sent. nil = no entry. (Amended 2026-11-26, late evening:
// it took a model and predicted nothing even then; now it takes what was sent.)
var provimuxAnswerEntry func(auth *Auth, sent cliproxyexecutor.ProvimuxSentRequest) string

// provimuxAnsweredRecord is set by sdk/cliproxy to the record of answered-by.md 4.4. publishProvimuxAnswer calls it
// for every statement that names an account and a model: with the credential, the model the client asked for
// (cliproxyexecutor.RequestedModelMetadataKey) and the statement's entry, "" for none. nil = nothing is recorded.
var provimuxAnsweredRecord func(auth *Auth, requestedModel, entry string)

// sdk/cliproxy/executor (layer: a new file; one field of the tracker and one line of WithUpstreamAttemptTracker)

// ProvimuxSentRequest is one request an attempt handed to its transport, as Provimux's decorator saw it.
type ProvimuxSentRequest struct {
	Scheme string // of the request's URL, lower case
	Host   string // its host, lower case, in ASCII, without the port
	Port   string // its port; "" = the scheme's default
	Model  string // the model id read from the request (answered-by.md 4.2); "" = none was read
	Signer string // which credential headers the request carries, by name only: "x-api-key", "authorization", "both", ""
	Status int    // the status of its response; 0 = no response arrived
}

// ProvimuxSent returns what the attempt of ctx sent, by step 2 of answered-by.md 4.3: the one host, model and signer
// that its observations with a model and a 2xx response agree on. ok is false when there is no such observation,
// when they differ, and when more were made than the tracker keeps.
func ProvimuxSent(ctx context.Context) (sent ProvimuxSentRequest, ok bool)

// provimuxObserveSent returns ctx with Provimux's decorator on it, around any decorator ctx already carries. Only
// WithUpstreamAttemptTracker calls it. The decorator notes each request of a context that has a tracker on that
// tracker and delegates; with models.answered-by off it is not attached.
func provimuxObserveSent(ctx context.Context) context.Context

// provimuxSentModel reads the model id of an outgoing request without consuming its body (GetBody): the top-level
// "model" of a JSON body, zstd decoded where Content-Encoding says so; the <id> of a URL path .../models/<id>:<method>;
// field 21 of a Connect message to Devin. "" where the request has none, has two that differ, or is in a form it
// does not know.
func provimuxSentModel(req *http.Request) string

// internal/provimux/modellists (from F1, keyed by what was sent)

// EntryProvider returns the providers.dev provider of a request by the gateway's provider of the credential that sent
// it, the credential's kind ("oauth" or "apikey"), the lower-case host the request went to and the names of its
// credential headers, or "" where the map of prices-and-cache.md 6.1 has no row.
func EntryProvider(provider, kind, host, signer string) string

// EntryModel is the model id of an entry of provider for the id that was sent.
func EntryModel(provider, sentModel string) string

// sdk/cliproxy

// provimuxEntryOfAnswer returns the entry of an answer of auth whose attempt sent sent, or "": "" for a credential
// held in memory only or one a plugin made or changed, for a URL that is not https on its default port, and where
// the map has no row.
func provimuxEntryOfAnswer(auth *coreauth.Auth, sent cliproxyexecutor.ProvimuxSentRequest) string

// internal/pluginhost

// ProvimuxPluginCredential reports whether a plugin made or changed auth: its storage is the plugin host's.
func ProvimuxPluginCredential(auth *coreauth.Auth) bool

// sdk/cliproxy/executor (the owner patch)

// AttemptReport is what the executor's usage reporter knew of one attempt when asked: the model the attempt was
// given or chose, the id the executor says it wrote into the outgoing request where it records one, the model the
// vendor's answer named so far, and the reasoning effort read from the outgoing request. The first two are not what
// was sent (answered-by.md 4.2): they stand in X-Provimux-Model-Sent only where ProvimuxSent has nothing.
type AttemptReport struct{ Model, UpstreamModel, ResponseModel, Effort string }

// AttemptHandleFromContext returns the attempt's tracker as a handle a reporter keeps; its methods do nothing
// without a tracker.
func AttemptHandleFromContext(ctx context.Context) *AttemptHandle
func (h *AttemptHandle) SetModel(model string)
func (h *AttemptHandle) SetUpstreamModel(model string)
func (h *AttemptHandle) SetResponseModel(model string)
func (h *AttemptHandle) SetEffort(effort string)

// AttemptReported returns what the attempt's reporter has told its tracker so far.
func AttemptReported(ctx context.Context) AttemptReport

// sdk/api/handlers

// WithAnsweredStatementCallback installs a callback that receives the whole statement of the result the execution
// layer returns, where WithAnsweredAuthCallback receives its first three values. A websocket turn uses it.
func WithAnsweredStatementCallback(ctx context.Context, callback func(logging.AuthStatement)) context.Context
```

Each body is a worker's.

### 8.2 Lines added to files that exist

| File | Whose lines there | Edit | Lines |
|---|---|---|---|
| `internal/logging/cpa_trace.go` | upstream's file, the owner's patch | `AuthStatement` gains the field `Provimux`; `OnTheWire` and `trimmedAuthStatement` keep it; `applyTraceHeader` calls `SetProvimuxAnswerHeaders` | +2, 2 changed |
| `sdk/cliproxy/auth/classification.go` | the owner's function `AuthStatement` | its statement carries the marker, so the six relays send it with no line of their own | 1 changed |
| `sdk/cliproxy/auth/conductor_execution.go` | the owner's function `publishAnsweredAuthMetadata` (`:3353-3366`) | one call after its callback. **None of the five places that call it changes** | +1 |
| `sdk/api/handlers/handlers_execution.go`, `handlers_stream.go` | beside the owner's three `reqMeta[…AnsweredAuthCallbackMetadataKey] = answered.set` | the second callback, three times | +3 |
| `sdk/api/handlers/handlers_answered.go` | the owner's file | `offerAnsweredAuth` hands a turn's whole-statement sink the statement | +4 |
| `sdk/api/handlers/openai/openai_responses_websocket.go` | beside the owner's sink (`:1069`) | the whole-statement sink, storing into the same value | +3 |
| `sdk/api/handlers/openai/openai_responses_websocket_forward.go` | beside the owner's two calls (`:286-287`) | remove and set the six fields | +2 |
| `sdk/api/handlers/openai/openai_models_accounts.go` | the owner's file | `account` on a row (built); `answered` on a row, through a function variable `sdk/cliproxy` sets (W6b) | +2, +4 |
| `internal/api/handlers/management/provimux_model_routes.go` | the layer's | `account` on a route (built); `answered` on a route (W6b) | +3, +5 |
| `sdk/cliproxy/executor/context.go` | upstream's tracker (`:20-25`) and `WithUpstreamAttemptTracker` (`:68-73`), with the owner's `pluginBytes` beside it. Not a protected path | the tracker gains the attempt's observations (one field); `WithUpstreamAttemptTracker` returns its context through `provimuxObserveSent` (one line) (W6a) | +2 |
| `internal/registry/catalog_config.go` | the layer's lines in upstream's struct | `AnsweredBy *bool` and its default | +5 |
| `internal/api/server_provimux.go` | the layer's | read the key at start | +1 |
| `config.example.yaml`, README | | the switch; a section "Who answered" | text |
| **Owner patch:** `sdk/cliproxy/executor/context.go` | upstream's tracker (`:20-25`), with the owner's `pluginBytes` beside it | one field | +1 |
| **Owner patch:** `internal/runtime/executor/helps/usage_helpers.go` | upstream's, **a protected path** | the reporter keeps the attempt's handle (one field, two lines where it is made) and tells it what it is told: `SetAttemptModel`, `ObserveResponseModel`, `SetResponseModel`, `SetUpstreamModel`, `SetTranslatedRequestMetadata`, one line each | +8 |

In all about 46 added and 3 changed lines in files that exist, 8 of the added in a protected path. No executor file, no translator and no auth file changes, and no request to a vendor changes by a byte. W6a and W6b add 11 of them, none in a protected path and none in the conductor: the place that states the answer is W3's own line.

**Why an owner patch, and what the other way is.** The reporter is the one place every executor tells which model the vendor's answer named and which effort went out, and it sits under `internal/runtime/executor`, which a layer commit may not touch (`guard.sh`). The patch changes no request. Without it no answer can say `vendor`: the gateway would have to read the model back out of a translated body, which finding 6 shows is not evidence. Decision 1. (Amended 2026-11-26, late evening: the patch is no longer what the sent model and the entry rest on. Those come from the request, in a layer commit, and W6 does not wait for the patch.)

### 8.3 What it costs to carry

Upstream commits that touched each file, and the stack's own [ran: `git log`; v8.0.30 is of 2026-10-15, v8.1.7 of 2026-11-27].

| File | Upstream, the 90 days to 2026-11-26 | Upstream, v8.0.30 to v8.1.7 | The stack's own | Where our lines sit |
|---|---|---|---|---|
| `sdk/cliproxy/auth/conductor_execution.go` | 79 | 49 | 8 | inside the owner's own function, not in upstream's loops |
| `internal/runtime/executor/helps/usage_helpers.go` | 37 | not counted | 0 | at the head of five small setters and in the constructor |
| `sdk/api/handlers/openai/openai_responses_websocket.go` | 30 | 17 | 3 | beside the owner's sink |
| `sdk/api/handlers/handlers_stream.go` | 23 | 12 | 3 | beside the owner's line |
| `sdk/api/handlers/openai/openai_responses_websocket_forward.go` | 20 | 13 | 6 | beside the owner's two calls |
| `sdk/api/handlers/handlers_execution.go` | 19 | 10 | 2 | beside the owner's two lines |
| `sdk/cliproxy/executor/context.go` | 4 | 3 | 1 | the tracker's struct, and the last line of `WithUpstreamAttemptTracker` |
| `sdk/httptransport/decorator.go` (called, not changed) | 1: the commit that made it, `51952a7b2` | 0 | 0 | W6a calls its `WithDecorator` and `Decorate`. It is upstream's public package for exactly this |
| `sdk/cliproxy/auth/classification.go` | 3 | 1 | 3 | the owner's function |
| `internal/logging/cpa_trace.go` | 2 | 1 | 8 | the owner's lines |
| `internal/registry/catalog_config.go` | 1 | not counted | 4 | the layer's own |

Every line but the eight in `usage_helpers.go` and the two in `context.go` sits beside a line the owner's patches already carry there, so it moves when that line moves and conflicts when that line conflicts, not more often. The eight are the new cost: a file upstream touches about three times a week, with our lines at the heads of functions it has not renamed in that time [infer from the commit subjects; the sync check shows the first time it is wrong].

### 8.4 What must hold

| Step | Must hold afterwards |
|---|---|
| The marker | every row of 2.3, each from a fixture login or key and no real one. The same marker after the config's keys are reordered, after the file is renamed or moved, after a restart with the kept key; another after another account's login is written into the same file, for every `a` kind and for Kimi; the two bold rows pinned as not changing. Two keys on one host differ. One login in two files is one marker. No key: no marker anywhere. Two installations give the same account two markers. A marker is never in a log line |
| In the lists | both lists give one account the same `account`; `/v1/models` without `include` is byte for byte `bc6f81d73`'s; with the switch off no row has `account`; `pui13/openapi.yaml` and its example grow in the same commit (W2 did so on its own branch, with `account` after `host`; the design branch has since gained `answered` after `recorded_cooldown` in the same schema and example: two places apart, and `git merge-tree` of the two branches is clean) |
| On the answer | the layer's wire test (`internal/pluginhost/provimux_answered_by_wire_test.go`, on `main` since `9d0786d95`: a credential, a plugin, a fallback and a plugin again on one connection, and three turns on one WebSocket) grows the six values: a credential's answer has them, a plugin's has none, a stale one never survives. The owner's matrix of each path's first commit (`internal/api/server_auth_first_byte_test.go`, `sdk/api/handlers/openai/auth_first_byte_test.go`) with two accounts of **one kind and one host**: each answer carries the marker of the one that answered; after a failover the second's; a keep-alive first, an error, a plugin, an interceptor, a plugin's own call: none of the six. The six are present together or absent together on every one of the 37 places. A WebSocket turn, a second turn on another account, a retry inside a turn. An upstream's own `provimux_*` fields never pass through. With the switch off every header and every event is byte for byte `bc6f81d73`'s. The confirm-sites test passes unchanged |
| The attempt's report (the owner patch) | per executor a stand-in vendor that answers **another** model id than the one asked: the report holds both, and for a stream it holds the reported one before the first chunk is handed on exactly on the executors section 3.3 says yes for, and not on the four it says no for. No request to any stand-in differs by a byte from the same test without the patch |
| The model on the answer | `vendor` and `not-confirmed` per row of 3.3; a forced alias leaves the body's model the client's name and the header the vendor's; a pool answers two requests with two sent ids; Devin's sent id carries the effort |
| What was sent (W6a) | **The one test that matters:** for every executor and every path it sends a model request on (answer, stream, token count where the vendor counts, compact, a retry), a stand-in vendor records the host it was called on and the body it received; the answer's `X-Provimux-Model-Sent` and `X-Provimux-Entry` are the map applied to **what the stand-in recorded**, never to what the test configured. Run once plain and once under a payload rule that sets `model` for one header only (finding 15): the two answers differ as the two bodies do. The same with a rule that deletes `model` (no entry), a zstd body, a redirect to another host (that host's row, or none), a base URL in a login's file and in its metadata (findings 3 and 17), xAI with `using_api` false, Codex with the second sign-in for a Codex client and for another, Vertex's two routes on one account (finding 13), Antigravity with the catalog stored between the list and the call and with its read failing (finding 16), a plugin that changes the account's provider (finding 14). An attempt with two answered model requests that differ: no entry. An upstream WebSocket turn: a statement, no entry. The reader of Devin's field 21 against upstream's own projection of the same bytes (`helps.FinalizeDevinPayload`). No request to any stand-in differs by a byte with the decorator on and off. With `models.answered-by` off the decorator is not attached |
| The map (W6a) | every row of `prices-and-cache.md` 6.1 and every "none" row, as F1's matrix has them (50 cases, 19 providers), keyed by the host that was sent; a host with a port, `http`, a subdomain of a named host: none |
| `answered` in the lists (W6b) | after an answer, the row of its account under the name asked for has the answer's entry (or `null`) as its newest item, **before the answer's first byte is written**: a test that reads the list from inside the stand-in's handler sees it only after the gateway's statement, and one that reads it after the first byte always sees it. Both lists give the same items, the client list with days. A name asked with a thinking suffix, with a prefix, through an alias, through a pool: each lands on the row whose id the account registers, and on no other. Two outcomes give two items, a fifth drops the oldest. Another account's login in the same file starts with nothing. After a restart with the kept key the record is there; without a store it is not. An item older than 90 days is gone. A name that is not served is in neither list and brings no item. With the switch off no row has `answered`. `/v1/models` without `include` is byte for byte `bc6f81d73`'s. Both contract files' examples validate, and the handlers' answers for the fixture validate against them |

**What the next review should try to break first** (Review B, of W6a and W6b). In this order, because the earlier ones are where a wrong entry could still come from:

1. **An answered request the decorator did not see, beside one it did.** An attempt whose answer came over a channel without an `http.RoundTripper` (an upstream WebSocket, a relay, a client an executor builds by hand) while the same attempt also sent a model request over HTTP that got a `2xx`. Step 2 would then name the HTTP one. The helpers found no such attempt [helper]; a Codex WebSocket turn that falls back to HTTP after a `426` is the nearest, and there the HTTP request is the one that answers.
2. **An observation on the wrong attempt.** A request built with another context than the attempt's (a detached context, a shared flight, a pooled goroutine), so that its observation lands on another attempt's tracker or on none.
3. **The credential's kind or signer lying.** A login's token configured as a key, on every executor where one host serves both (Claude is handled by the header; is there another?). A key marked as not the paid API in some field step 3 does not read. A credential a plugin changed whose storage a later native refresh replaced.
4. **A body the reader misreads.** A `model` that is not a string; a body that is not JSON but parses; two `model` keys; a Connect message whose first frame is not the request; an encoding it does not decode.
5. **The record on a wrong row**: a name with a suffix or a prefix, an alias that is also another account's id, a pooled alias, a forced alias, "serve only my names" on and off.
6. **What `answered` gives away** to a holder of a client key about other clients' use.
7. Bytes: every list and every answer with the switch off, and every request to a vendor with it on.

### 8.5 What of F1's branch is kept, and what a sync no longer carries

`provimux-figures-f1-rebase` at `4f14c2cff`: three commits on `ecf2cc2b4`, 2,223 added lines in 16 files. It is not merged.

| F1's | Lines | Kept? |
|---|---|---|
| `internal/provimux/modellists/figures_map.go`: `EntryProvider`, `EntryModel`, `EntryRef`, `GoogleVertexHost`, the host tables | 181 | **Kept**, as W6a's first commit, keyed by what was sent: `host` is the host the request went to, so the "no base URL" alternatives go and `LoginBackend` becomes rows of the map; `EntryProvider` gains the signer; `EntryModel` loses the stripping of `copilot/` (the sent id is bare) |
| `figures_map_test.go`: the matrix | 172 | **Kept**, re-keyed the same way. Both reviews found it right: 50 cases, 19 providers |
| The three kinds of no entry | | **Kept as three, redefined** (`prices-and-cache.md` 6.1): no row; not read; not vouched for. "A plugin lists the model" and "the request decides" are gone |
| `sdk/cliproxy/auth/provimux_upstream_model.go`: the mirror `ProvimuxUpstreamModel`, its probe manager, `ProvimuxAntigravityVariantByEffort`, two accessors | 74 | **Dropped** |
| `provimux_upstream_model_test.go`: the drift test | 202 | **Dropped.** There is nothing left to drift from |
| `sdk/cliproxy/provimux_entry_refs.go`: `provimuxEntryOf`, the Vertex route's prediction, `provimuxNativeModel`, `provimuxLoginOwnBackend` | 158 | **Dropped.** Steps 3 to 5 of the rule are about 40 lines in `provimux_answer_entry.go` |
| `provimux_entry_refs_test.go`, `provimux_entry_cases_test.go`, `provimux_entry_per_request_test.go` | 996 | **Dropped as tests of rows.** Their fixtures (the provider matrix against real registration, the usage-record comparison, the stand-ins) are the start of W6a's wire test, which asserts on answers |
| `internal/pluginhost/provimux_plugin_models.go` | 36 | **Dropped.** One predicate replaces it (`ProvimuxPluginCredential`) |
| The two hooks in `openai_models_accounts.go` and `provimux_model_routes.go` (`entry`), and `provimux_model_routes_entry_test.go` | 112 | **Replaced** by the same hooks, and the same test, for `answered` |
| `pui13/openapi.yaml`: `entry` on a route | 14 | **Replaced** by `answered`, on the design branch with this amendment |
| `test/provimux/model_list_entries_test.go`, the parity test's growth | 318 | **Rewritten** for `answered`; the byte-identity test without `include` is kept as it is |
| Its edits to `prices-and-cache.md` 6.1 | 20 | **Superseded** by this amendment. The branch's copy is not merged |

**What a sync carried with F1, and carries now.** To predict, F1 called these internals of upstream, and each could change what it means without failing a build [ran: `git log v8.0.30..v8.1.7` per file]:

| F1 called | Upstream changed its file, v8.0.30 to v8.1.7 | Now |
|---|---|---|
| `executionModelCandidatesWithAlias` on a manager copied field by field (three fields; a fourth is the hole of note 9) | `conductor_models.go`: 8 commits | not called |
| `antigravityModelHasThinkingGroup` | `antigravity_model_enums.go`: 4 | not called |
| `IsCodexUseSIWC`, the login's base URL fields | `types.go`: 27 | not called |
| `helps.ShouldExecuteVertexOpenAPIForRequest`, `NormalizeVertexOpenAPIModel`, `VertexAuthBaseURL` | `vertex_openapi.go`: 7 | not called |
| The plugin host's executor adapter and its model lists | `pluginhost/host.go`: 13 | one type check on a credential's storage |
| `helps.IsGoogleVertexHost`, `registry.LookupDevinModel` | | still called: a host rule and a catalog lookup, on what was sent |

Now the layer calls `sdk/httptransport` (upstream's public package, unchanged since it was made) and reads three wire forms: a JSON body's `model`, a `…/models/<id>:<method>` path, Devin's field 21.

| When upstream | F1 as built | Now |
|---|---|---|
| decides a model or a host in one more late place (a new payload rule, a new metadata field, a new discovery) | a wrong row, found by a review | nothing to do: it is in the request |
| adds an executor, or a new wire form for the model | a row from the mirror, right or wrong | **no entry** on its answers until a reader or a map row is added. W6a's wire test fails for an executor it does not cover, so a sync sees it |
| moves an executor to a channel without an `http.RoundTripper` | a row still | no entry on that route; the same test fails |
| changes `WithUpstreamAttemptTracker` | not touched | one line to carry |
| renumbers Devin's protobuf field | not touched | the reader's test against upstream's own projection fails |

## 9. The steps, in order

Amended 2026-11-26, late evening. W1 to W3 are built (`provimux-answered-by`, `9b71b947f`) and this amendment changes nothing they hold. **F1 folds into W6**: its map and the map's tests are W6a's first commit, and the rest of its branch is dropped (8.5). **W6 now comes before W4 and W5** and needs no owner patch: what was sent is read from the request, not taken from the reporter. Each step is small enough to review alone.

| Step | Kind | Holds | Depends on | What exists after it |
|---|---|---|---|---|
| W1 | layer, new files only | the key, kept and read; the marker of 2.2 and 2.3; no wire change | | built |
| W2 | layer | `account` in both lists; the switch | W1 | built |
| W3 | layer | the marker on every answer: the statement's new field, the headers, the WebSocket fields, the relays | W1 | built |
| Review A | | W1 to W3 together: what a marker gives away, the key's life, every row of 5.3, bytes with the switch off | | |
| **W6a** | layer | what was sent (4.2, 4.3): the decorator and the attempt's observations; the map and its matrix (from F1); `X-Provimux-Entry` on the answer, and with it `X-Provimux-Model-Sent`, `X-Provimux-Model` and `X-Provimux-Model-Basis: not-confirmed`, which the contract requires beside an entry | W3 | every answer sent over HTTP names its entry, read from its own request |
| **W6b** | layer | `answered` (4.4): the record, kept per marker and name; on both lists; `pui13/openapi.yaml` and its example as this amendment has them | W6a | a tool, the console and Pi have what each account last answered as |
| Review B | | W6a and W6b: the list of 8.4, in its order | | |
| F2 | layer | `prices-and-cache.md` 6.2, 6.3: the figures are fetched and held. It needs nothing of W6 and can be built beside it | | |
| F3 | layer | `include=figures`: the entries the rows' `answered` name | W6b, F2 | contract 1 can be released |
| W4 | **owner patch** | the attempt's report: one new file, one field, eight lines in the reporter. No wire change | the owner's yes (decision 1), and the stack re-ordered to hold it below the layer | |
| W5 | layer | the model the vendor named: basis `vendor` where it did; the effort. Where W6a read nothing (an upstream WebSocket), the model the executor was given, as `X-Provimux-Model-Sent` | W4, W6a | a tool can tell whether the vendor confirmed the model. The measuring needs this one |
| W6s | **owner patch** | what was sent over an upstream WebSocket: one line where a message is handed to the socket, beside the `MarkUpstreamAttempt` there (`codex_websockets_execute.go:261`, `codex_websockets_stream.go:221`, `codex_websockets_duplex.go:626`, `xai_websockets_executor.go:817`, and their resends), noting the socket's URL and the frame's `model` as an observation that counts as answered once the turn's first event arrives. Its own short design first: which statement a later turn of one duplex stream belongs to is not verified (section 11) | W6a; the owner's yes (decision 12) | an answer over an upstream WebSocket names its entry too |
| W7 | docs | the README's section, the release's note, the contract file as built | W6b | |
| After | other repositories | pi-provimux (F5 reads the statement and `answered`), groundtruth (the measuring reads the statement), provimux-ui (F4 reads `answered`) | a release with W3, or with W6b | |

**Order.** W6a, W6b, then F3 once F2 is there; F2 beside them. W4 and W5 run on their own line as soon as the owner says yes to the patch, and block nothing of the figures. W6s is last and optional. A release can carry W1 to W3 alone, or W1 to W6b, or all of it.

## 10. Decisions that are the owner's

| # | Decision | Recommendation | The other way costs |
|---|---|---|---|
| 1 | An owner patch of eight lines in the usage reporter, under a protected path, so that the answer can say which model the vendor named | **Yes.** It changes no request, and it is the one place the fact exists | Without it no answer can ever say `vendor`: the model is `not-confirmed` everywhere, and no answer says its effort. The entry and the sent model no longer wait for it (amended 2026-11-26, late evening) |
| 2 | The names: `X-Provimux-*` and `provimux_*`, beside the `X-CPA-AUTH-*` the owner's patches named | **`X-Provimux-*`**: the marker needs a key only Provimux keeps, and a name upstream may one day use for something else cannot collide | `X-CPA-AUTH-ACCOUNT` and so on reads as one family, and would be upstream's to redefine |
| 3 | One marker per gateway, the same for every client key | **Per gateway** | Per key, two holders cannot compare notes, which tells them nothing the list does not; a tool with two keys can then prove nothing, and the operator needs a second marker |
| 4 | Always on, with `models.answered-by: false` to turn it off | **Always on** | Asked for per request protects nothing and puts a header on every request of every tool |
| 5 | `X-CPA-TRACE-ID` keeps upstream's unkeyed index of the selected credential | **Leave it, and say in the README that it is not the account that answered.** It is upstream's header and format | Taking the index out of it is three lines in upstream's own function and changes a value operators match against their logs. Leaving it leaves this: whoever holds a key of the gateway can check a guessed API key, or a guessed path of a login file, against it |
| 6 | An API key's identity is its base URL and the key, not its host and the key | **The base URL** | With the host alone, two accounts behind one gateway host and one key would share a marker |
| 7 | Meta and Antigravity are told apart by the e-mail their vendor names, inside the keyed hash and never shown | **Yes** | Without it both are `s`: a replaced Meta login keeps its marker, which is the case the measuring could not see |
| 8 | A Codex login with the second sign-in bound has no entry when a client other than Codex's own is answered by `api.openai.com` | **No entry**, until providers.dev measures that route as an entry of its own | Calling it `openai-codex` gives it the ChatGPT backend's cache lifetime; calling it `openai` gives a subscription the paid API's price |
| 9 | The words of the README section and the release's note | the owner's | |
| 10 | What a list row carries, now that no entry is predicted (4.5) | **`answered`: what the account's answers for the name said, newest first, with the day.** Never an `entry` | No entry information on a row at all: the console's page of accounts has no figure to show, ever, and `include=figures` cannot choose which entries to send. A predicted entry for "safe" cases: there is no such set, and it is what failed twice |
| 11 | Whether the record outlives a restart | **Kept**, 90 days, in the store that keeps the marker's key | Lost at a restart: after each release every account line of the console is empty until that name is used again, and Pi prices from models.dev until then. Kept costs a small write per item per day, and shows an old item with its old day after the configuration changed, which is what a record is |
| 12 | W6s: an owner patch of about six lines in four files of the WebSocket executors, so that an answer over an upstream WebSocket names its entry | **Yes if a Codex or xAI account of his has `websockets` on**, which his configuration says and I did not read. Else leave it | Without it such an answer has a statement and no entry: never a wrong one. With it, six more lines in a protected path that upstream changes often |
| 13 | The time of an item: the day in the client list, the time behind the management key | **The day** | The time to the second in the client list tells one key holder when another was at work |
| 14 | A model a plugin put on an account's list, sent by the account's own executor to the account's own host with a credential the gateway made | **It has the entry of what was sent.** A plugin's executor answering names nobody, and a credential a plugin made or changed has no entry: those two stay | Keeping F1's rule ("a plugin lists it: none") needs the plugin host's lists read at every answer, and withholds entries that are right (the second review's note 18) |
| 15 | The xAI row asks for more than a key and the host: no `using_api` other than true anywhere on the account | **Yes.** `api.x.ai` also takes requests from a credential that is not the paid API (its compact and speech requests go there, `xai_executor_request.go:338-344`), and the request does not show which it is | Without it, a credential marked as not the paid API gets the paid API's figures on those requests |

## 11. What I ran and read, and what is not verified

| Check | Result |
|---|---|
| The design branch moved onto `main` at `bc6f81d73` (`git rebase --onto origin/main aa76f801b`): four commits, no conflict | done |
| Every `file:line` of `prices-and-cache.md` compared between `aa76f801b` and `bc6f81d73` by a script: 20 cited ranges | all 20 unchanged |
| `git log v8.1.4..v8.1.7`: 74 commits | two touch what the designs assume: the Home local plugin executor (`6acc778c0`, v8.1.6: one more path that names nobody) and pool positions kept per credential (`c3570f755`, v8.1.5). The Antigravity variant by effort is older (`2f86992c9`, v8.1.3, with the attempt's model note): the figures design had missed it, and F1's review found it |
| Read: the owner's patch `f4fda78ea` and the state after its followers: `cpa_trace.go`, `handlers_answered.go`, `handlers_context.go:115-135`, `classification.go:60-124`, `publishAnsweredAuthMetadata`, the tracker in `executor/context.go`, the WebSocket's sink and the two functions that stamp `response.created`, the confirm-sites test | sections 1, 5, 8 |
| Read: the usage reporter (`usage_helpers.go:33-80`, `:96-175`, `:283-340`, `:420-438`); the Codex destination rule (`types.go:996-1014`, `:1064-1093`); Claude's made-up account id (`claude_executor_auth.go:84-128`); Savings' secret (`saver.go:205-235`, `clientkey.go`); the account list's handler; the switches' struct | sections 2, 3, 4, 6 |
| Three helper agents, read-only, each returning file and line: what every executor knows of the model (15 executors); every place an answer is written and the WebSocket; the identity each kind of credential holds | sections 2.3, 3.3, 5; tagged [helper] |
| `git log` per file for 8.3, mine, against a helper's independent count | the same numbers |
| `prices-and-cache/openapi.yaml` through `openapi-spec-validator`; the three examples against its schemas with unknown properties forbidden; planted faults | section 7.1's files; the results are in the commit's message |

| For the amendment of the late evening: three helper agents, read-only, each returning file and line, over 15 executors and the conductor: which helper builds each client and whether the decorator of `sdk/httptransport` wraps it; how each destination is chosen; where the model sits in the outgoing bytes; the order of the payload rules, the reporter's model, the request log and the send; every other request an execution makes on the same context; what a plugin's change of an account leaves on it | sections 4.1 to 4.3; tagged [helper]. I read myself: `sdk/httptransport/decorator.go`, `helps/proxy_helpers.go:20-75`, `helps/logging_helpers.go:25-135`, the reporter's model setters, `executor/context.go`, `newUpstreamAttemptContext` and `publishAnsweredAuthMetadata`, `classification.go`, the Claude executor's choice of credential header, the Codex request's compression, Devin's model id, F1's four source files, W1 to W3 as built |
| `git log v8.0.30..v8.1.7` on the files F1 called and the two W6a touches | 8.3, 8.5 |
| Both contract files through `openapi-spec-validator`; the four examples against their schemas with unknown properties forbidden; cross-checks and planted faults | the results are in the commit's message |

**Not verified.**

| Point | Who settles it |
|---|---|
| That every model request an executor sends is built with the attempt's context, so that its observation lands on the attempt's tracker (the helpers found every context derived by adding values; a stream's is a child of the attempt's) | W6a's wire test, per executor and path: an answer without an observation fails it |
| That every such request has `GetBody` (the helpers found bodies made with `bytes.NewReader`; a multipart upload may not) | W6a: without it nothing is read, and there is no entry |
| That a refresh inside an attempt runs under a tracker of its own on every path [helper, read in part] | W6a. It matters only for a refresh that carries a `model`, and none does |
| That the credential headers of a Claude request are final when the transport is handed it (the executor fixes their casing just before) | W6a's test, with a login's token configured as a key |
| That a credential a plugin made or changed keeps the plugin host's storage after the account's own executor refreshed it | W6a's test for finding 14, with a refresh between the change and the answer. If it does not, step 3 needs a mark set where the plugin's update is committed (`service_executors.go:715-740`), one line |
| That Devin's sent model id is always an id of Devin's catalog, so that its variant resolves to its base model | W6a, over the catalog. Where it is not, the answer has no entry |
| That the model the client asked for (`RequestedModelMetadataKey`) is on the call's metadata on every path that states an answer, a WebSocket turn included | W6b. Without it nothing is recorded for that answer |
| Which statement a later turn on one duplex upstream WebSocket belongs to | W6s's own design |
| Whether an account of the owner has `websockets` on | his configuration; decision 12 |
| That the context an executor makes its reporter with is the attempt's, tracker included, on every executor | W4's test, per executor |
| That a Codex stream kept open over several turns (the duplex stream) cannot name another model on a later `response.created` than the statement made at its start | W5's test. If it can, the model fields are left off every `response.created` after the first of such a stream |
| The exact text of the effort the reporter reads (`high`, a budget) per executor | W5; the contract says only "as read from the outgoing request" |
| That Devin's request id is never the same string for two efforts | W5's test over Devin's catalog |
| That every vendor names its real variant in the answer (Antigravity's `modelVersion` for a `-high` variant) | not needed: where it does not, the basis is `not-confirmed` or the two ids differ, and both are said |
| That a Claude login always has an organisation id once its profile was read | W1: without one it is `s`, which is safe |
| That the key-value store is open before the first request is served, in every start mode | W1 |
| Whether the owner's Codex login has the second sign-in bound | his file; it decides what a Pi measurement on that login measures (section 4.6, the last paragraph) |
| Nothing was built or run against a gateway | every step's tests |
