# aiquota

A fast personal terminal CLI that renders your remaining **Claude** and **Codex**
subscription quota as a compact card: session (5h) and weekly windows,
percent remaining, and a human-readable reset countdown.

```
╭────────────────────────────────╮  ╭────────────────────────────────╮
│  Claude                    Team│  │  Codex                     Team│
│                                │  │                                │
│  Session ████░░░░░░  43% left  │  │  Session █████████░  85% left  │
│    resets in 1h 8m             │  │    resets in 18m               │
│                                │  │                                │
│  Weekly  ███████░░░  67% left  │  │  Weekly  ███████░░░  71% left  │
│    resets in 5d 18h            │  │    resets in 5d 17h            │
╰────────────────────────────────╯  ╰────────────────────────────────╯
updated 3s ago
```

Cards sit side by side when the terminal is wide enough, stack vertically
otherwise, and fall back to a borderless compact layout below 34 columns. Each
card shows only the windows its provider actually reports.

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

### Working on a local checkout

An alias runs whatever binary is already installed, so an edit in your checkout
does not show up until you reinstall. If you want `aq` to always reflect the
working tree, replace the alias with a launcher that reinstalls when a source
file is newer than the installed binary:

```zsh
aq() {
  emulate -L zsh
  local repo="$HOME/path/to/aiquota"
  local binary="$(go env GOPATH)/bin/aiquota"
  local source
  local -a sources=(
    "$repo/go.mod" "$repo/go.sum"
    "$repo"/cmd/**/*(N) "$repo"/internal/**/*(N)
  )

  if [[ ! -x "$binary" ]]; then
    (cd "$repo" && go install ./cmd/aiquota) || return 1
  else
    for source in "${sources[@]}"; do
      if [[ "$source" -nt "$binary" ]]; then
        (cd "$repo" && go install ./cmd/aiquota) || return 1
        break
      fi
    done
  fi

  command "$binary" "$@"
}
```

The staleness check costs one glob over `cmd/` and `internal/`, so a cache hit
stays well under a second. There is no `go:embed` asset outside those trees, so
every build input is covered.

Requires Go 1.26+ and, per provider:

- **Claude**: the native `claude` CLI installed, on `PATH`, and logged in.
- **Codex**: the native `codex` CLI installed, on `PATH`, and logged in.

A provider whose prerequisite is missing renders a failed card; the others are
unaffected. macOS (darwin/arm64) is the primary target; Linux is exercised on
WSL through the installer below.

### Install the Mac source build on WSL

From this repository checkout on the Mac, run the explicit installer:

```sh
make install-wsl
```

The SSH host defaults to `wsl`. Set `AIQUOTA_WSL_HOST` for another host, or call
the underlying script directly when an argument is more convenient:

```sh
AIQUOTA_WSL_HOST=my-wsl-host make install-wsl
scripts/install-wsl.sh my-wsl-host
```

The helper cross-builds `linux/amd64`, transfers it to a unique temporary file
under `/home/kkamji/go/bin`, smoke-tests it, atomically replaces `aiquota`, and
compares the Mac `shasum -a 256` result with WSL `sha256sum`. If post-install
verification fails, it restores the previous binary. Successful installs leave
no backup or temporary binary behind.

WSL invocation does not conditionally rebuild or pull source. The WSL machine
does not have this source checkout, and an invocation-time wrapper would make a
status command mutate Git state or depend on network availability. Installation
is therefore an explicit deployment step from the Mac source of truth.

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
frames. Terminals narrower than the 34-cell card automatically use a borderless
compact layout so the watch frame does not wrap. The default interval is 60s and
the floor is 2s.

```sh
aq -w                 # watch, refresh every 60s
aq -w -i 300          # watch, refresh every 5 minutes
```

## Source boundaries (why this is safe)

aiquota **never reads, writes, refreshes, prints, caches, or transmits** OAuth
credentials, browser cookies, Keychain values, API keys, or credential files. It
does not run `login`/`logout`, never refreshes tokens, never opens a browser, and
never sends a model prompt. The CLI-backed adapters only invoke the installed
native CLIs in read-only status modes and consume their output:

- **Codex** (`internal/provider/codex`): spawns `codex app-server` and speaks its
  JSON-RPC protocol - `initialize`, `account/read` with `refreshToken: false`,
  then `account/rateLimits/read`. Windows are mapped to Session/Weekly by their
  actual `windowDurationMins`, not field order. Hard 6s timeout.
- **Claude** (`internal/provider/claude`): the `claude` CLI has no stable JSON
  quota command, so this drives the interactive `/usage` view read-only through a
  PTY (allocated via the system `script` binary) with tools disabled. The PTY is
  sized with `stty rows 80 cols 120` before the CLI starts, because `script`
  under a non-tty parent otherwise hands the CLI an 80x24 terminal and the
  `/usage` view (which now carries a session cost summary and a plugin footprint
  table above the quota windows) clips to a scrollable panel that hides the
  weekly window. It runs from the user's home directory so slash-command input
  works consistently on Linux without loading project instructions, captures
  only a bounded in-memory terminal stream, and stops when the complete usage
  view is parseable. Hard 6s
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
- Carried-over snapshots are named: a provider whose kept value is more than a
  minute behind the render is appended to the footer as `codex 6h old`, with the
  category of the refresh that failed and left it behind when one was recorded
  (`claude 6d 18h old (unsupported)`), so a card full of hours-old numbers is
  never read as freshly queried and the reason is visible without `--debug`.
  A window whose reset time has already passed describes a window that no
  longer exists: its bar and percent are replaced by `stale` and the reset line
  reads `reset passed` rather than counting down to `now`.
- All-provider failure: when a prior healthy cache exists, its original save time
  is preserved instead of presenting unchanged data as newly refreshed.
- Stored fields are non-sensitive only: provider, plan, remaining percent, reset
  timestamp, source label, updated timestamp, and safe failure categories (the
  current failure, and for a kept snapshot the last refresh's failure).

## Failure behavior

One provider failing never suppresses the other cards. A failed card shows the
provider name and a short safe status - `Not signed in`, `Timed out`,
`Unsupported quota response`, or `Unavailable` - and never raw error text. When
a prior good snapshot exists, the card keeps showing it and the footer names
the age and the failure instead (see cache semantics above).

## Troubleshooting

Start with `aiquota --refresh --debug`. It prints one safe line per provider to
stderr, e.g. `provider=claude elapsed=2.7s result=ok`, and the `result` value is
the same category the footer shows in parentheses.

| Symptom | Meaning | What to check |
| --- | --- | --- |
| footer `claude 6d 18h old (unsupported)` | Every recent refresh parsed nothing usable, so the card is a carried-over snapshot. | The CLI's `/usage` layout changed. Run `claude` and `/usage` by hand and compare with the `Current session` / `Current week (all models)` lines the parser expects. |
| footer `... (timed_out)` | The provider did not answer within its ceiling (Claude 6s, Codex 6s). | Slow network or a CLI that hangs on startup. Run the CLI by hand once. |
| footer `... (unavailable)` or card `Unavailable` | The CLI is not on the `PATH` of the shell that ran `aiquota`, or it exited early. | Non-interactive shells (cron, `ssh host cmd`) often lack the interactive `PATH`. Export the CLI's directory before calling `aiquota`. |
| card `Not signed in` | The CLI reported no usable session. | Log in with the native CLI. aiquota never runs login itself. |
| `Session stale` with `reset passed` | The snapshot predates the window's reset, so its percent is not current. | Same cause as the footer note above; the footer says which refresh failed. |

The cache lives at `~/Library/Caches/aiquota/cache.json` on macOS and
`~/.cache/aiquota/cache.json` on Linux. Deleting it is always safe; the next
`aiquota` simply queries again.

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
AIQUOTA_INTEGRATION=1 go test ./internal/provider/claude/ -run TestLiveProbeSafeEvidence -v
```

They spawn the real CLIs read-only and assert only safe outcomes. The second
command logs which parse stages the live Claude `/usage` screen passed
(`session_seen`, `week_seen`, `percent_markers`, ...), which is the quickest way
to see where a CLI layout change broke the parser.

## Known limitations

- **Claude adapter is terminal-scraping**, because no stable JSON quota API is
  exposed. It parses the `/usage` view's `Current session` / `Current week (all
  models)` lines and reads the plan from the startup banner (`... · Claude
  Team`). If Anthropic changes that view's wording or layout, parsing degrades
  safely to `Unsupported quota response` rather than guessing, and the card
  keeps its last good snapshot with the footer naming the failure. The `Not
  signed in` wording is best-effort: the real signed-out string was not verified
  (doing so would require touching credentials), so an unrecognized signed-out
  state also degrades to `Unsupported`.
- **Claude PTY uses the platform `script` form**: BSD/macOS
  (`script -q /dev/null sh -c '...'`) and util-linux (`script -q -c "..."
  /dev/null`) are selected by `runtime.GOOS`, and both size the PTY with `stty`
  before exec'ing the CLI. macOS is the primary tested target; the Linux path is
  exercised on WSL.
- **Codex adapter maps only the documented `rateLimits` bucket.** If a future
  server omits it and returns only multi-bucket data, Fetch returns `Unsupported`
  rather than guessing which bucket is the quota. The `codex app-server` must
  respond within 6s or the card shows `Timed out`.
- No daemon, menu-bar app, dashboard, database, or cost accounting - by design.

## License

Released under the [MIT License](LICENSE).
