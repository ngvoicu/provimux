# State of the restart drain: `GET /v8/management/provimux/restart-drain`

The README section "Restarts and upgrades" says what a stop does. This note is the contract of the state the console
reads, so that it never keeps its own copy of the rule. The rule is the code's: `ProvimuxStopDrainSeconds` and
`ProvimuxStopDrainDefaultSeconds` (`internal/config/provimux_restart_drain.go`), read by upstream's stop
(`api.Server.Stop`). Handler: `internal/api/handlers/management/provimux_restart_drain.go`; route in
`internal/api/server_provimux.go` (a layer file: no upstream line), which hands the handler the server's own
configuration: the one upstream's stop reads. Read-only: no setting of its own, and nothing about
a stop changes. The setting is read and written at `/v8/management/config/server/restart-drain-seconds`, as before.

A Provimux gateway without this path (404) is one released before it: the console then shows no control. The console
reads this path again before every write.

## Fields

| Field | Meaning |
|---|---|
| `seconds` | The budget a stop now is given: the configuration the running process holds after its hot reloads (`server.restart-drain-seconds` when set, 0: no wait; otherwise `default`). A reload the gateway refused left the configuration before it, so `seconds` stays as it was. |
| `waits_at_most` | `seconds` within `stop_limit`: what a stop waits at most for the answers in flight. |
| `set` | Whether the configuration file sets `server.restart-drain-seconds` (or `restart-drain-seconds` at the top, the layout before v8). `null` does not set it. |
| `written` | The value as the file writes it, when `set`, as YAML: also when out of range or not a number; a quoted value keeps its quotes (`"10"`, refused, never reads like `10`). |
| `written_accepted` | When `set`: whether the gateway accepts `written`. `false`: a reload of this file is refused (the running process keeps `seconds`), and a start with it fails at loading the configuration. |
| `written_seconds` | What `written` gives (a whole number outside `min`..`max` is used as the nearest bound); absent when the gateway refuses it. |
| `written_clamped` | `written` is a whole number outside `min`..`max` (the gateway logs a warning once). |
| `written_error` | Why `written` is refused (not a whole number: `ten`, `"10"`, `30.0`). |
| `default` | The budget when the setting is not set: 25, or `server.shutdown-timeout` when that is larger. Not capped: `waits_at_most` is. |
| `shutdown_timeout` | `server.shutdown-timeout` in the configuration the gateway runs with. |
| `min`, `max` | The bounds of the setting: 0 and 30. |
| `stop_limit` | Upstream's bound on the whole stop, 30 seconds (the shutdown context of `Service.Run`). |
| `file_error` | Present when the configuration file could not be read; `set` and `written` are then unknown. |

`seconds` and `written_seconds` differ when the file changed after the gateway read it and the reload was refused or
has not happened yet.

## Examples

Each from a configuration file under `server:` (`TestProvimuxRestartDrainState`, the outputs as logged).

Not set:

```json
{"seconds":25,"waits_at_most":25,"set":false,"default":25,"shutdown_timeout":0,"min":0,"max":30,"stop_limit":30}
```

`restart-drain-seconds: 0`:

```json
{"seconds":0,"waits_at_most":0,"set":true,"written":"0","written_accepted":true,"written_seconds":0,"default":25,"shutdown_timeout":0,"min":0,"max":30,"stop_limit":30}
```

`restart-drain-seconds: 10`:

```json
{"seconds":10,"waits_at_most":10,"set":true,"written":"10","written_accepted":true,"written_seconds":10,"default":25,"shutdown_timeout":0,"min":0,"max":30,"stop_limit":30}
```

`restart-drain-seconds: 45` (used as 30):

```json
{"seconds":30,"waits_at_most":30,"set":true,"written":"45","written_accepted":true,"written_seconds":30,"written_clamped":true,"default":25,"shutdown_timeout":0,"min":0,"max":30,"stop_limit":30}
```

A refused value written while an earlier valid one runs: the gateway started with `restart-drain-seconds: 10`, then
the file was edited to `"10"` (in quotes) or to `30.0`. Each reload is refused; the running process keeps 10, and a
stop now waits up to 10 (`TestProvimuxRestartDrainStateFollowsTheRunningProcess` checks the stop's own line); a cold
start with either file fails at loading the configuration:

```json
{"seconds":10,"waits_at_most":10,"set":true,"written":"\"10\"","written_accepted":false,"written_error":"server.restart-drain-seconds must be a whole number of seconds from 0 to 30, got \"10\"","default":25,"shutdown_timeout":0,"min":0,"max":30,"stop_limit":30}
```

```json
{"seconds":10,"waits_at_most":10,"set":true,"written":"30.0","written_accepted":false,"written_error":"server.restart-drain-seconds must be a whole number of seconds from 0 to 30, got \"30.0\"","default":25,"shutdown_timeout":0,"min":0,"max":30,"stop_limit":30}
```

`restart-drain-seconds: ten`, written while the gateway runs with the setting unset (the reload is refused):

```json
{"seconds":25,"waits_at_most":25,"set":true,"written":"ten","written_accepted":false,"written_error":"server.restart-drain-seconds must be a whole number of seconds from 0 to 30, got \"ten\"","default":25,"shutdown_timeout":0,"min":0,"max":30,"stop_limit":30}
```

`shutdown-timeout: 28`, the drain not set:

```json
{"seconds":28,"waits_at_most":28,"set":false,"default":28,"shutdown_timeout":28,"min":0,"max":30,"stop_limit":30}
```

`shutdown-timeout: 60`, the drain not set (the budget is 60; the stop's own bound makes it 30 at most):

```json
{"seconds":60,"waits_at_most":30,"set":false,"default":60,"shutdown_timeout":60,"min":0,"max":30,"stop_limit":30}
```

## Notes for a later release

- With `shutdown-timeout: 60` and the drain not set, the released stop's log line says "stopping: waiting up to 60
  seconds for the answers in flight" while the wait is 30 (upstream's bound on the whole stop). The line could say
  `waits_at_most`. Not changed here: a stop does what it did.
