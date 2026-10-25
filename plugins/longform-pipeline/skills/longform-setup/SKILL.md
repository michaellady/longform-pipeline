---
name: longform-setup
description: "Onboarding wizard for the longform-pipeline plugin: creates a brand profile, the iCloud inbox folder, stores the OpusClip key, adds the OpusClip MCP, captures OpusClip account ids and Buffer tag ids, builds the iPhone Shortcuts (Photos album 'Mark for <Brand>' and the iCloud inbox fallback), installs the launchd watcher, checks Full Disk Access for the Photos album feed, and dry-runs a 30-second fixture. Triggers: /longform-pipeline:longform-setup, 'set up longform', 'add a new brand profile', 'install the longform watcher', 'reinstall launchd for longform', 'longform onboarding'."
user_invocable: true
---

# longform-setup

Runs the interactive wizard at `${CLAUDE_PLUGIN_ROOT}/scripts/setup.sh`. The wizard is
a bash script that talks to the human directly (hidden input for secrets, browser
opens, Enter-to-continue), so it must run in a real terminal, not through a captured
Bash tool call.

## What to do

1. Check prerequisites and say which are missing: `jq`, `ffmpeg`, `claude`, and the
   `longform` binary (`${CLAUDE_PLUGIN_ROOT}/scripts/longform`, or `make install` from
   the repo). Missing `jq`/`ffmpeg` -> `brew install jq ffmpeg`. Missing binary -> tell
   the user to run `make install` in the repo checkout, or download the release
   tarball (docs/install.md). Do not block on the binary: stages 1–9 work without it.
   If the brand will use a Photos album feed, also check `osxphotos`
   (`command -v osxphotos`, `~/.local/bin/osxphotos`, `/opt/homebrew/bin/osxphotos`);
   missing -> `pipx install osxphotos` (or `brew install osxphotos`).
2. Launch the wizard where the human can type:
   - If a terminal tool is available (`mcp__terminal__run_in_terminal`), run
     `bash "${CLAUDE_PLUGIN_ROOT}/scripts/setup.sh"` there and tell the user to follow
     the prompts in that tab.
   - Otherwise print the exact command for them to paste into Terminal:
     `bash "${CLAUDE_PLUGIN_ROOT}/scripts/setup.sh"`.
3. Wait. When they say it finished, confirm with `longform status` and
   `launchctl print gui/$(id -u)/com.$(id -un).longform-watch | head -20`.
   For an album profile, `longform photos-watch --profile <p> --dry-run` lists the
   album (exit 69 = osxphotos missing or no Full Disk Access for the calling process);
   whether launchd's own run can read it shows in
   `~/Library/Logs/longform-pipeline/watch.err.log` (`watch: photos <p>: ...`).

Subcommands you may run directly (non-interactive):

| Command | Effect |
|---|---|
| `setup.sh launchd-install` | re-render the LaunchAgent from every profile and reload it |
| `setup.sh launchd-uninstall` | unload and delete the LaunchAgent |
| `setup.sh fixture [path]` | generate the 30-second ffmpeg test video |

## The 12 stages (so you can answer questions about them)

| # | Stage | Writes |
|---|---|---|
| 1 | Profile name, brand display/site, orientation, stitching | memory only |
| 2 | Inbox folder | `~/Library/Mobile Documents/iCloud~is~workflow~my~workflows/Documents/longform-inbox/<profile>/` |
| 3 | `OPUSCLIP_API_KEY` (hidden) | `~/.config/longform-pipeline/env` (0600) |
| 4 | Checks `claude mcp list` for the claude.ai OpusClip **connector** (the only OpusClip MCP this plugin uses); if absent, tells the user to connect it once via `/mcp` in a session or claude.ai → Settings → Connectors. The watcher never uses it: launchd uploads go through the REST API with the stage 3 key. | nothing written |
| 5 | Human step: connect TikTok, X and anything else in the OpusClip dashboard (neither was connected when probed; the skill skips platforms with no account id) | nothing |
| 6 | OpusClip accounts via `longform opus accounts`, template id (default `665fefb18ce49592eaeff8a0`, the only brand template: portrait, captions, no overlay), curation model | memory |
| 7 | Buffer tags `format:video-quote`, `format:video-carousel`, `format:video-still`, `format:video-frame-carousel` and channel ids | memory |
| 8 | Slack webhook (optional, hidden) | `~/.config/longform-pipeline/env` |
| 9 | Asks for the **Photos album** the phone tags recordings into (blank = iCloud Drive inbox only; `-` turns an existing album feed off). With an album: resolves `osxphotos` (warns `pipx install osxphotos` or `brew install osxphotos` when missing), tells the person to create the album with that exact name in Photos on the phone, and builds the one-tap "Mark for <Brand>" shortcut (`longform shortcut --kind mark --album <name> --open`). Always builds the "Send to <Brand> inbox" shortcut too, the fallback feed (docs/shortcut-recipe.md) | `~/.config/longform-pipeline/shortcuts/` |
| 10 | Profile JSON, every field of `internal/model/profile.go`, including `photos` (`enabled` = album set; `download_missing` and `library` kept from the existing profile) | `~/.config/longform-pipeline/profiles/<profile>.json` |
| 11 | LaunchAgent from `launchd/com.USER.longform-watch.plist.tmpl` (PATH includes `~/.local/bin` for pipx's osxphotos). With an album it then runs `longform photos-watch --profile <p> --dry-run` and walks the **Full Disk Access** grant for the binary launchd runs (System Settings → Privacy & Security → Full Disk Access → + → the binary; re-check after an update replaces it), plus Photos → Settings → iCloud → "Download Originals to this Mac" | `~/Library/LaunchAgents/com.<user>.longform-watch.plist` |
| 12 | Fixture + `longform stage --profile <p> --file fixture.mp4` + `longform status` | `~/.config/longform-pipeline/fixtures/sample-30s.mp4`, one job |

Re-running the wizard is safe: existing profile values and env values are offered as
defaults (Enter keeps them).

## Rules

- Never ask the user to paste a secret into chat. Secrets go through the wizard's
  hidden prompt into the env file; refer to them by name only.
- Never edit a profile by hand to "fix" the wizard; re-run it (defaults carry over).
- Never run the wizard through a captured Bash call with piped answers; the hidden
  prompts and `read -e` need a TTY.
- Full Disk Access is a person-only step in System Settings; never try to grant it
  (or edit TCC) from a tool. Point at the exact binary path the wizard printed.
- Do not install launchd by hand with `launchctl load`; use `setup.sh launchd-install`
  (or `make launchd-install`) so the WatchPaths are rendered from every profile.
