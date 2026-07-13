# aiquota

A fast personal terminal CLI that renders your remaining **Claude** and **Codex**
subscription quota as a compact two-column card: session (5h) and weekly windows,
percent remaining, and a human-readable reset countdown.

```
╭────────────────────────────────╮  ╭────────────────────────────────╮
│  Claude                        │  │  Codex                  prolite│
│                                │  │                                │
│  Session ████░░░░░░  43% left  │  │  Session █████████░  85% left  │
│    resets in 1h 8m             │  │    resets in 18m               │
│                                │  │                                │
│  Weekly  ███████░░░  67% left  │  │  Weekly  ███████░░░  71% left  │
│    resets in 5d 18h            │  │    resets in 5d 17h            │
╰────────────────────────────────╯  ╰────────────────────────────────╯
updated 3s ago
```

## Install

```sh
go install github.com/kkamji98/aiquota/cmd/aiquota@latest
```

This installs the `aiquota` binary to `$(go env GOPATH)/bin`. Since it is a
status command you run often, a short shell alias is handy:

```sh
echo "alias aq='aiquota'" >> ~/.zshrc
```

## Build (from source)

```sh
go build -o aiquota ./cmd/aiquota
```

Requires Go 1.26+, macOS (darwin/arm64), and the native `codex` and `claude`
CLIs installed and logged in.

## Commands

| Command            | Behavior                                                                 |
| ------------------ | ------------------------------------------------------------------------ |
| `aiquota`          | Render from cache if it is fresh (<= 20s); otherwise refresh, then render.|
| `aiquota --refresh` / `-r` | Query both providers concurrently now and update the cache.       |
| `aiquota --json` / `-j` | Print normalized results as JSON (safe fields only).              |
| `aiquota --debug` / `-d` | Also print safe per-provider timing and failure categories to stderr. |
| `aiquota --no-color` / `-n` | Disable ANSI color (also honored via the `NO_COLOR` env convention). |
| `aiquota --watch` / `-w` | Keep the card on screen and auto-refresh on an interval until Ctrl-C. |
| `aiquota --interval` / `-i` | Set the watch refresh interval in seconds (default 60, floor 2). |
| `aiquota --watch --json` | Emit one newline-delimited JSON record per refresh, with no ANSI bytes. |

### Watch mode

`aiquota --watch` (or `-w`) renders once, then re-queries and redraws every
`--interval` (or `-i`) seconds (default 60) until you press Ctrl-C, which restores
the cursor and exits cleanly. The previous card remains visible until the next
refresh is ready, so slow provider queries do not expose a blank terminal. The
next interval starts after a refresh completes, so slow provider queries never
overlap or queue another refresh. It is handy pinned in a tmux pane or a side
terminal. `--watch --json` writes newline-delimited JSON instead of terminal
control sequences. With interactive `--watch --debug`, safe timing lines are
included below the card in each complete redraw instead of being written between
frames. The default interval is 60s and the floor is 2s.

```sh
aq -w                 # watch, refresh every 60s
aq -w -i 300          # watch, refresh every 5 minutes
```

## Source boundaries (why this is safe)

aiquota **never reads, writes, refreshes, prints, caches, or transmits** OAuth
credentials, browser cookies, Keychain values, API keys, or credential files. It
does not run `login`/`logout`, never refreshes tokens, never opens a browser, and
never sends a model prompt. It only invokes the installed native CLIs in
read-only status modes and consumes their output:

- **Codex** (`internal/provider/codex`): spawns `codex app-server` and speaks its
  JSON-RPC protocol - `initialize`, `account/read` with `refreshToken: false`,
  then `account/rateLimits/read`. Windows are mapped to Session/Weekly by their
  actual `windowDurationMins`, not field order. Hard 2s timeout.
- **Claude** (`internal/provider/claude`): the `claude` CLI has no stable JSON
  quota command, so this drives the interactive `/usage` view read-only through a
  PTY (allocated via the system `script` binary) with tools disabled. It runs
  from the user's home directory so slash-command input works consistently on
  Linux without loading project instructions, captures only a bounded in-memory
  terminal stream, and stops when the complete usage view is parseable. Hard 6s
  timeout; shutdown sends SIGTERM to the child process group, then uses SIGKILL
  only if the bounded grace period expires.

Raw child-process output (JSON-RPC payloads, terminal text) is held only in
memory and is **never** written to the cache, logs, debug output, or test
fixtures. All committed test fixtures are hand-authored sanitized synthetic data.

## Cache semantics

- Location: the OS user cache dir (macOS: `~/Library/Caches/aiquota/cache.json`),
  never the project or any credential directory.
- Freshness: a plain `aiquota` serves the cache when it is at most 20s old and
  labels it `updated Ns ago`, so cached data is never presented as a fresh query.
  Cache hits render in well under 20ms.
- Partial success: on `--refresh`, only providers that succeed overwrite their
  cache entry. A provider that fails keeps its last good cached value; if there is
  no prior value, the safe failure is recorded. Writes are atomic (temp + rename).
- All-provider failure: when a prior healthy cache exists, its original save time
  is preserved instead of presenting unchanged data as newly refreshed.
- Stored fields are non-sensitive only: provider, plan, remaining percent, reset
  timestamp, source label, updated timestamp, and a safe failure category.

## Failure behavior

One provider failing never suppresses the other card. A failed card shows the
provider name and a short safe status - `Not signed in`, `Timed out`,
`Unsupported quota response`, or `Unavailable` - and never raw error text.

## Testing

```sh
go test ./...            # unit + fixture + renderer + cache tests (no network, no CLIs)
go test -run '^$' -bench . -benchmem ./...
go vet ./...
go build ./cmd/aiquota
```

Real-account integration probes are opt-in and skipped by the ordinary suite:

```sh
AIQUOTA_INTEGRATION=1 go test ./internal/integration/ -v
```

They spawn the real CLIs read-only and assert only safe outcomes.

## Known limitations

- **Claude adapter is terminal-scraping**, because no stable JSON quota API is
  exposed. It parses the `/usage` view's `Current session` / `Current week` lines.
  If Anthropic changes that view's wording or layout, parsing degrades safely to
  `Unsupported quota response` rather than guessing. The `Not signed in` wording
  is best-effort: the real signed-out string was not verified (doing so would
  require touching credentials), so an unrecognized signed-out state also degrades
  to `Unsupported`. Plan/tier is not shown for Claude (not reliably present in the
  view).
- **Claude PTY uses the platform `script` form**: BSD/macOS
  (`script -q /dev/null <cmd>`) and util-linux (`script -q -c "<cmd>" /dev/null`)
  are selected by `runtime.GOOS`. macOS is the primary tested target; the Linux
  path follows util-linux syntax and is exercised on WSL.
- **Codex adapter maps only the documented `rateLimits` bucket.** If a future
  server omits it and returns only multi-bucket data, Fetch returns `Unsupported`
  rather than guessing which bucket is the quota. The `codex app-server` must
  respond within 2s or the card shows `Timed out`.
- No daemon, menu-bar app, dashboard, database, or cost accounting - by design.

## License

Released under the [MIT License](LICENSE).
