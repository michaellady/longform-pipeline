# Install

macOS only (launchd, iCloud Drive, `brctl`). Apple Silicon and Intel binaries are
released.

## 1. Prerequisites

```bash
brew install jq ffmpeg whisper-cpp
# optional: shellcheck (lint the wizard), go (build from source)
```

- Homebrew's ffmpeg 9 ships without the `vidstabdetect` and `zscale` filters.
  `longform prepare` copes: stabilisation falls back to `deshake` (or is
  skipped when neither filter exists) and HDR recordings are tonemapped with
  the plain `tonemap` filter. The method used is recorded under
  `extra.prepare.stabilize_method` and `extra.prepare.tonemap_filter` in
  `job.json`. Install an ffmpeg build with libvidstab and zimg to get the
  two-pass stabiliser and the more accurate tonemap back.
- Claude Code with the Buffer MCP connected (statics) and, after the wizard, the
  OpusClip MCP (interactive sessions).
- An OpusClip plan with API access (Pro or Enterprise). The key goes into
  `~/.config/longform-pipeline/env` by the wizard, never into the repo or a profile.

## 2. Plugin

In Claude Code:

```
/plugin marketplace add michaellady/longform-pipeline
/plugin install longform-pipeline
```

That gives you `/longform-pipeline:longform-setup` and `/longform-pipeline:longform-publish`, the launchd template, and
the wizard script under the plugin cache. The `longform` binary is separate (next
step) because a plugin cache is not a place for a 10 MB executable.

## 3. Binary

Either from a checkout:

```bash
git clone https://github.com/michaellady/longform-pipeline
cd longform-pipeline
make install          # downloads the latest release tarball for your arch into
                      # plugins/longform-pipeline/scripts/longform, or builds it if
                      # go is present and no release matches
longform version      # if you symlinked it onto PATH; else use the full path
```

Or straight from a release:

```bash
tag=$(gh release view --repo michaellady/longform-pipeline --json tagName -q .tagName)
arch=$(uname -m | sed s/x86_64/amd64/)
curl -sSL "https://github.com/michaellady/longform-pipeline/releases/download/$tag/longform_${tag}_darwin_${arch}.tar.gz" | tar -xz
sudo mv longform /usr/local/bin/
```

The wizard finds the binary at `$LONGFORM_BIN`, then
`<plugin>/scripts/longform`, then `PATH`.

## 4. Wizard

```
/longform-pipeline:longform-setup
```

or in a terminal:

```bash
bash "$(claude plugin path longform-pipeline 2>/dev/null || echo plugins/longform-pipeline)/scripts/setup.sh"
```

Twelve stages: profile and brand, inbox folder, OpusClip key, OpusClip MCP, connect
TikTok and X in the OpusClip dashboard, OpusClip accounts, Buffer tags, Slack
(optional), iPhone Shortcut (`docs/shortcut-recipe.md`), profile JSON
(`docs/profile-schema.md`), launchd, dry run on a generated 30-second fixture.
Re-running keeps existing values as defaults.

## 5. Verify

```bash
longform status
launchctl print gui/$(id -u)/com.$(id -un).longform-watch | head
tail ~/Library/Logs/longform-pipeline/watch.log
```

Then record a real video, Share → the Shortcut, and watch `longform status` move
from `staged` to `clipped`. The brief lands in `review/brief.md`, in Slack if
configured, and as a macOS notification.

## Make targets

| Target | Effect |
|---|---|
| `make build` | `go build` into `plugins/longform-pipeline/scripts/longform` |
| `make test` / `make vet` / `make check` | Go tests, vet, both plus build |
| `make install` | fetch the release binary for this arch (fallback: build) |
| `make launchd-install` | render the LaunchAgent from every profile and load it |
| `make launchd-uninstall` | unload and delete the LaunchAgent |
| `make fixture` | generate `plugins/longform-pipeline/fixtures/sample-30s.mp4` with ffmpeg |
| `make release-dry` | build both darwin tarballs plus sha256 into `dist/` without tagging |

## Command table

Every `longform` subcommand the skills and wizard call. Exit codes follow sysexits:
`0` ok, `64` usage, `65` validation failure (verdict JSON on stdout), `70` internal,
`75` retry later.

| Command | Called by | Effect |
|---|---|---|
| `longform version` | release workflow, you | print the build version |
| `longform status [--json]` | wizard stage 12, skill, you | list jobs and states |
| `longform watch --once` | launchd | scan every profile inbox, advance every job one step |
| `longform stage --profile <p> --file <mp4>` | wizard dry run | create a job from one file without the watcher |
| `longform brief render --job <dir>` | skill, draft | read `review/draft.json`, write `review/brief.md`, state `drafted` |
| `longform brief parse-decision --job <dir> "<grammar>"` | skill, approve | write `review/decision.json` and `review/schedule.json`, state `approved` |
| `longform brief render-statics --job <dir>` | skill, approve | render PNG/PDF statics from the profile templates into `statics/` |
| `longform slots propose --profile <p> --cells <file> --existing <file>` | skill, draft | fill `publish_at` per cadence and free slots |
| `longform gate validate --profile <p> --cells <file>` | skill, approve | caps, durations, profanity, cadence; exit 0 or 65 with verdict JSON |
| `longform opus accounts` | wizard stage 6 | list connected OpusClip social accounts and ids |
| `longform opus usage` | skill, draft | running credit estimate (2 x source minutes per job); true balance via the MCP `opusclip_get_usage` |
| `longform opus schedule --cells <file>` | skill, approve | `POST /publish-schedules` per cell at 1 req/s, verbatim responses on stdout |
| `longform opus strip-overlay --project <id> --clip <id> [--wait] [--force]` | binary at `clipped`; skill if a clip still shows an overlay | free overlay-strip re-render per short; skips when no overlay is present |
| `longform opus export` | you | export URLs for clips |
| `longform manifest init --job <dir>` | skill, approve | create `manifest.json` |
| `longform manifest ensure-clip --job <dir>` | skill, approve | add a clip entry if missing |
| `longform manifest append-post --job <dir>` | skill, approve | append one verbatim API response (stdin) |
| `longform manifest expire --job <dir>` | watcher | drop held clips past `backlog_days` |
| `longform notify --job <dir> --event <state> --message "..."` | skill | macOS notification plus Slack when configured |
| `longform yt-upload --file ... --title ... --description ... --chapters-file ... --thumbnail ... --privacy ... --publish-at ...` | skill, approve (fallback only) | YouTube Data API upload when the native OpusClip post cannot carry chapters or a custom thumbnail |

## Uninstall

```bash
make launchd-uninstall
rm -rf ~/.config/longform-pipeline            # profiles, env, templates, fixtures
rm -rf ~/Library/Application\ Support/longform-pipeline   # jobs
rm -rf ~/Library/Logs/longform-pipeline
```

In Claude Code: `/plugin uninstall longform-pipeline`.

## Headless editorial sessions need a logged-in `claude`

`longform launch-session` (called by the watcher once a job is `clipped`) runs
`claude -p "/longform-pipeline:longform-publish <jobdir>"` as the launchd user. That only works when:

- the `claude` CLI on the watcher's PATH is logged in for the config directory it
  will use (`CLAUDE_CONFIG_DIR` if set in the LaunchAgent's environment, else
  `~/.claude`); run `claude` once interactively from a normal terminal and sign in,
- the plugin is installed in that same config directory (`claude plugin install
  longform-pipeline@longform-pipeline` from that terminal), so the skill exists there.

A session started from a sandboxed shell or a different config directory reports
`Not logged in · Please run /login` in `<LogRoot>/<job>.session.err` and the job
stays `clipped`; nothing is lost, re-run `longform launch-session <jobdir>` after
signing in.

## Installing from a local checkout

```bash
make build
claude plugin marketplace add /path/to/longform-pipeline
claude plugin install longform-pipeline@longform-pipeline
```

The install copies the plugin directory, binary included, into the plugin cache;
after rebuilding, uninstall and install again to refresh the copy. The setup
wizard can be run from the installed copy so the LaunchAgent points at the cached
binary rather than a working tree:

```bash
bash ~/.claude/plugins/cache/longform-pipeline/longform-pipeline/<version>/scripts/setup.sh
```

### Symptoms in `<LogRoot>/<job>.session.json`

| `result` text | Meaning | Fix |
|---|---|---|
| `Unknown command: /longform-publish` | the plugin is not installed in the config directory this `claude` uses, or an old binary invoked the bare name | install the plugin there; skills are `/longform-pipeline:<skill>` |
| `Failed to authenticate: OAuth session expired and could not be refreshed` | the CLI's sign-in lapsed | run `claude` in a terminal, `/login`, then relaunch |
| `Not logged in · Please run /login` | never signed in for that config directory | same |

Relaunch a job whose session failed with `longform launch-session --force <jobdir>`;
the watcher launches each job only once on its own.

## Running everything inside the Claude desktop app

The Claude desktop app's Code tab runs with its own config directory (its shell
exports `CLAUDE_CONFIG_DIR`, for example `~/.claude-secondary`), separate from the
terminal CLI's `~/.claude`. Plugins, MCP entries and sign-ins live per config
directory, so the whole pipeline must agree on one:

1. Install the plugin from the app's Code tab shell (so it lands in the app's config).
2. Run the wizard from that same shell. It checks for the OpusClip connector and
   writes `CLAUDE_CONFIG_DIR` into the LaunchAgent, so the headless editorial
   session that launchd starts uses the app's plugin, connector and sign-in.
3. Connect OpusClip once as a claude.ai **connector** (`/mcp` in a session, or
   claude.ai → Settings → Connectors), then sign in on OpusClip's consent page.
   `claude mcp list` then shows `claude.ai OpusClip`. The connector follows your
   account rather than a config directory, so every session signed in as you sees
   it, including the headless one launchd starts. The connector is the only
   OpusClip MCP this plugin uses; the watcher's own uploads go through the REST
   API with the key from the wizard, because launchd cannot complete OAuth.

To move the pipeline to the terminal CLI instead, re-run the wizard from a terminal
where `CLAUDE_CONFIG_DIR` is unset; `make launchd-install` re-renders the agent.

### The executable launchd runs

LaunchAgents load no shell aliases and only the PATH written in the plist, so the
wizard records the `claude` executable it found (`command -v claude`, else
`~/.local/bin/claude`) as `LONGFORM_CLAUDE` in the agent, next to
`CLAUDE_CONFIG_DIR`. A shell alias such as
`alias claude-secondary='CLAUDE_CONFIG_DIR=~/.claude-secondary claude'` resolves to
that same binary; only the config directory differs, so the primary profile is
untouched. Run `/login` once in a session started the same way (the alias, or the
app's own terminal, which already sets `CLAUDE_CONFIG_DIR`) and launchd sessions
share that sign-in.
