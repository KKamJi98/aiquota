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
| `aiquota --refresh`| Query both providers concurrently now and update the cache.              |
| `aiquota --json`   | Print normalized results as JSON (safe fields only).                     |
| `aiquota --debug`  | Also print safe per-provider timing and failure categories to stderr.    |
| `aiquota --no-color`| Disable ANSI color (also honored via the `NO_COLOR` env convention).    |
| `aiquota --watch` / `-w` | Keep the card on screen and auto-refresh on an interval until Ctrl-C. |
| `aiquota -w --interval 300` | Watch with a custom refresh interval in seconds (default 60, floor 2). |

### Watch mode

`aiquota --watch` (or `-w`) clears the screen, renders once, then re-queries and
redraws every `--interval` seconds (default 60) until you press Ctrl-C, which
restores the cursor and exits cleanly. It is handy pinned in a tmux pane or a
side terminal. Each tick performs a full refresh, so keep the interval at a value
that does not hammer the native CLIs (the Claude `/usage` probe takes a few
seconds); 60s is a comfortable default and the floor is 2s.

```sh
aq -w                 # watch, refresh every 60s
aq -w --interval 300  # watch, refresh every 5 minutes
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
  PTY (allocated via the system `script` binary) with tools disabled in a
  throwaway temp directory, captures the rendered text, and parses it. Hard 6s
  timeout; the child process group is killed at capture end.

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
- Stored fields are non-sensitive only: provider, plan, remaining percent, reset
  timestamp, source label, updated timestamp, and a safe failure category.

## Failure behavior

One provider failing never suppresses the other card. A failed card shows the
provider name and a short safe status - `Not signed in`, `Timed out`,
`Unsupported quota response`, or `Unavailable` - and never raw error text.

## Testing

```sh
go test ./...            # unit + fixture + renderer + cache tests (no network, no CLIs)
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
- **Claude PTY uses the BSD/macOS `script` form** (`script -q /dev/null <cmd>`).
  Linux support would need the `script -q -c "<cmd>" /dev/null` form or a PTY
  library.
- **Codex adapter maps only the documented `rateLimits` bucket.** If a future
  server omits it and returns only multi-bucket data, Fetch returns `Unsupported`
  rather than guessing which bucket is the quota. The `codex app-server` must
  respond within 2s or the card shows `Timed out`.
- No daemon, menu-bar app, dashboard, database, or cost accounting - by design.
