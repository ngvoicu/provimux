# Session affinity across a restart (`routing.session-affinity-persist`)

The README section "Conversations keep their account across a restart" says what a user gets. This file says how, for
whoever changes the code or reviews it. Code: `sdk/cliproxy/provimux_affinity_persist.go` (files, lock, writer, state),
`sdk/cliproxy/auth/provimux_affinity_snapshot.go` (capture, restore, pin, account identity),
`sdk/cliproxy/provimux_affinity_owner_{unix,windows}.go` (owner, mode, lock per system).

## 1. Shape

- **On by default whenever session affinity is on**: `provimuxAffinityPersistDefault` (one constant). A user with
  `routing.session-affinity: true` gets it without setting anything; `routing.session-affinity-persist: false`, in the
  configuration or the console, turns it off. Tests cover both values of the constant (`TestProvimuxAffinityDefault`)
  and what an upgrade does when nothing is set (`TestProvimuxAffinityUpgradeSetsNothing`). Note (2026-11-27): the
  default is on, by the owner's rule.
- **Only the session cache is kept**, never the LCP matcher. Its groups are copied under the cache's read lock in the
  cache's eviction order, encrypted and written after the lock is released: when the process is told to stop (just
  before upstream's `StopAutoRefresh`, before the HTTP drain), and every 5 minutes. Nothing runs on a request.
- **Restored once at start**, before the server exists, in the same order, with the selection sequence kept. Each entry
  lives until the earlier of its own expiry and its last use plus the TTL configured now, and never past the
  snapshot's time plus that TTL. The file is not deleted after a restore: it stays until the next write replaces it.
- **Off means gone**: whenever it is not in effect (setting false, session affinity off, Home mode, KV store
  disabled), our files are removed at the start, at every check (5 s) and at the stop, and none is written. Removal
  needs our key and the store's lock (see 3); files that do not authenticate, and a store another running Provimux
  holds, are left.

## 2. Files and what "ours" means

| File | Content |
|---|---|
| `<kv path>.affinity-key` | `PMXAFFKEY2\n`, 32 random bytes, 16-byte check value = HMAC-SHA256(secret, "provimux affinity key check v2")[:16]. Created with `O_EXCL`, mode 0600. |
| `<kv path>.affinity` | `PMXAFF2\n`, 12-byte nonce, AES-256-GCM of the JSON state under HMAC-SHA256(secret, "provimux affinity snapshot v2"), the header as associated data. |
| `.<snapshot name>.tmp-*` | a snapshot being written; renamed over the snapshot, or removed. |
| `.<snapshot name>.probe-*` | created and removed at once to see that the directory can be written. |

**Ours means it authenticates.** A key file is ours only when it is a regular file with exactly that format, length
and check value. A snapshot or temporary file is ours only when it decrypts and authenticates under our key. A header is
not proof: a file that merely begins with our header bytes, a damaged file of ours, a snapshot without its key or under
another key, a link or a directory are not ours. Nothing that is not ours is ever renamed over, replaced or removed;
nothing is written or restored while one is at either name, and the state says `foreign-file` with the path. The user
removes a damaged snapshot by hand. Before the rename that publishes a snapshot, the target is authenticated again.

**The key's owner and mode** (macOS and Linux) are checked at each use: owned by another user, or with group or other
bits that cannot be cleared, the key is not used (`key-not-private`); a key of the right owner with a wider mode is set
to 0600 (`key_note`). Access control lists and the directory's permissions are not inspected; the directory already
holds the login files under the same protection. Windows: neither check (no Unix owner or mode); the folder's ACL
decides.

## 3. One writer per store

The writer holds an exclusive lock on the key file (`flock` on Unix, `LockFileEx` on one byte past the end on Windows)
from its first check with a key to the end of its service; the kernel releases it when the process ends. A second
service of the same store, in this process or another, writes nothing while the lock is held (state `in-use`) and takes
the lock on a later check. The snapshot can be read without the lock: a restarted process restores even while an old
one lingers.

A stop waits at most 1.5 s for its final write. When it gives up, the runtime is marked cancelled. Just before the
rename, the writer checks that it still holds the lock and was not cancelled; otherwise it removes its temporary file
and stops. The lock is released only when that write has returned. So a write that was given up never lands, in the same
process (`TestProvimuxAffinityGivenUpWriteNeverLands`, `TestProvimuxAffinityOneWriterPerStore`) or across processes
(`TestProvimuxAffinityStalledWriterAcrossProcesses`).

## 4. The same account, or nothing

Accounts are named in the file by an HMAC, under the installation key, of a stable vendor identity: API key and host;
Claude account and organization ids; Codex workspace and user ids; Devin user id. Without one (Meta), nothing is
written. A restored entry goes only to the auth whose identity has that HMAC now; two auths with the same identity get
none.

While running, every auth ID is read at every check (5 s) and twice around each write's copy. An auth ID first seen
after the start is trusted only from that moment; one whose identity changes, becomes unreadable, or is missing at any
check is **tainted for the rest of the run**: none of its bindings is written again, those made after the change
included. A binding is written only when its auth ID was watched, untainted, from the binding's last use to the write.
This closes the three paths the third review found (`TestProvimuxAffinityLoginRotationGaps`): a replacement between
the check and the write's own read (the second read taints it), a login added and replaced before its first check (its
watch starts after the binding's last use), an old request completing after the replacement was seen (tainted, whatever
the last use). It prefers leaving out too much: a binding made on a new login in the seconds before its first check is
left out.

## 5. Across builds: the pin

Each file carries a pin: sample requests (`provimuxAffinityPinSamples`) and what the writing build's selector made of
them, run through `session.Enrich` and `Pick` on a fresh selector: the keys, the binding, the eviction order, the
lifetime left after a hit, a result, an expiry and an eviction, and the default capacity. The reading build runs the
same samples and compares.

- A sample of a **rule** every entry follows (`Rule: true`: model key, recency, refresh, expiry to the millisecond,
  failure, capacity and its default) that differs, a different entry layout, or a step the reader does not know:
  **the file is ignored**.
- A sample of **one kind of conversation** that differs: the entries holding a key of the kinds that sample shows (the
  identity's prefix: `claude`, `header`, `pck`, `msg`, ...) are **left out**; the rest is restored
  (`restored_at_start.other_build`, `other_build_kinds`).
- Each kind of key is sampled with ordinary and unusual ids: mixed case, surrounding space, a control character, longer
  than upstream accepts, just under its bound, non-ASCII, empty after trimming.

`TestProvimuxAffinityPinAcrossBuilds` builds the auth package again with one upstream edit at a time (`go test
-overlay`, a scratch copy of one file) and checks the verdict: key syntax, refresh key, recency, sequence, lifetimes and
expiry void the file; a fallback-key or a Claude naming edit and the third review's case folding of `X-Session-ID`
leave out their kinds; the third review's 2-second expiry grace and smaller default capacity void the file; a log line
and a hit's own renewal (Pick binds again with one TTL) are harmless. Upstream v8.1.7 itself changed one kind
(`msg`, a conversation with neither an id nor a derived identity): an upgrade from a v8.1.4-based build leaves that
kind out and restores the rest.

**What the pin cannot promise:** a naming change that no sample exercises passes. Its effect is bounded: that
conversation goes to another account once after the upgrade, where its cache is cold; it is never an error.

## 6. State: `GET /v8/management/provimux/session-affinity-persist`

```json
{
  "setting": true,              // routing.session-affinity-persist as it applies (the default, on, when not set)
  "set": false,                 // whether the configuration sets it
  "in_effect": true,
  "reason": "",                 // when not in effect, see below
  "detail": "",                 // which file and why, for the file reasons
  "snapshot": "/path/kv.db.affinity",
  "key": "/path/kv.db.affinity-key",
  "key_note": "",               // the key's mode was set back to 0600
  "last_written_at": "2026-11-26T21:00:00Z",
  "last_bindings": 0,
  "left_out_account_unverified": 0,
  "last_error": "",
  "restored_at_start": {"sessions": 0, "expired": 0, "unknown_accounts": 0, "other_build": 0, "other_build_kinds": []},
  "start_note": ""
}
```

Reasons: from the configuration `session-affinity-off`, `home-mode`, `kv-store-off`, `switch-off` (set to false);
from the files `foreign-file`, `key-not-private`, `directory-not-writable`, `path-unusable`, `in-use`. The state is a
copy in memory; reading it never waits on the disk. With two services in one process (an SDK embedding), the endpoint
shows the one started last.

## 7. Known limits, kept on purpose

- A KV path moved while running: the new path is used from the next check; files at the old path stay until removed by
  hand or the path is moved back.
- A TTL lengthened and then shortened across two restarts can leave out an entry that had lifetime left (the last use
  is taken as expiry minus the TTL of the snapshot that carried it). It leaves out, never misroutes.
- In a directory that cannot be written nothing is restored, because the file could never be replaced.
