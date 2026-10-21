# longform-pipeline

Record a long-form video on your phone, let OpusClip clip it, and fan out the full
video, the shorts, and static posts to every social surface after one approval.

**Code moves bytes and enforces caps; a skill makes editorial calls; you approve one
brief.**

```
iPhone ──Shortcut──▶ iCloud Drive/longform-inbox/<profile>/
                         │  launchd: longform watch --once
                         │  stage · prepare (remux, tonemap, vidstab, whisper, beep)
                         │  upload: 2 OpusClip projects (shorts + full, skipCurate)
                         │  poll until clipped
                         ▼
                 claude -p "/longform-pipeline:longform-publish <job>"      ← picks clips, writes copy,
                         │                                   statics, adversarial review
                         ▼
                 review/brief.md  → Slack + macOS notification → you: "approve ..."
                         │
                         ▼
                 gates (caps, durations, profanity, cadence) → OpusClip native schedule
                 (clips + full video) · Buffer queue (statics) · manifest.json
```

## Layout

- `cmd/longform`, `internal/` — the deterministic Go transport: watch an inbox, prepare
  media, talk to OpusClip, enforce publishing gates, hand off to a Claude Code skill.
  Every state transition lives here and is idempotent on re-run.
- `plugins/longform-pipeline` — the Claude Code plugin:
  - `skills/longform-publish` — the thin editorial skill (draft and approve phases)
  - `skills/longform-setup` — the onboarding wizard (`scripts/setup.sh`)
  - `launchd/com.USER.longform-watch.plist.tmpl` — the watcher LaunchAgent template
  - `fixtures/` — how the 30-second dry-run video is generated (no binaries committed)
  - `scripts/longform` — the released binary, fetched by `make install` (gitignored)
- `docs/` — `install.md` (plugin, binary, wizard, the full `longform` command table),
  `profile-schema.md` (every profile field with examples), `shortcut-recipe.md` (the
  iPhone Shortcut).
- `.github/workflows/release.yml` — tag `v*` builds darwin arm64 and amd64 tarballs
  and publishes a release with a plain-English summary.

Brand profiles, templates, voice corpora, keys and tag ids live under
`~/.config/longform-pipeline/`, never in this repo. Jobs live under
`~/Library/Application Support/longform-pipeline/jobs/`.

## Quick start

```
/plugin marketplace add michaellady/longform-pipeline
/plugin install longform-pipeline
```

```bash
git clone https://github.com/michaellady/longform-pipeline && cd longform-pipeline
make install
```

```
/longform-pipeline:longform-setup
```

Then record, Share → the Shortcut, and wait for the brief. Approve with
`/longform-pipeline:longform-publish approve <slug> "approve"` (or `approve except 3,7`, `hold linkedin`,
`drop 4`, `swap thumbnail 2`, `decide d1 <option>`, `reject`).

## Job states

```
detected → staged → prepared → uploaded → clipped → drafted → approved → scheduled → monitored → closed
```

The binary owns every transition. The skill writes one file, `review/draft.json`, and
asks the binary to render the brief and parse your decision.

## Non-negotiables

- Shorts are 30, 60, or 90 s buckets; OpusClip "Auto" is never used.
- Per-platform cadence from the profile; never a 6th post on one day, roll to the next.
- YouTube `title` is the short curated title (≤ 100 chars); every other platform takes
  the caption as `title`.
- Every caption ends with `[opus:<clip_id>]` on its own line.
- Every scheduled cell's verbatim API response lands in `manifest.json`.
- Gates return exit codes that are read, never bypassed.

## Development

```bash
make check          # vet + test + build
make release-dry    # both darwin tarballs into dist/, no tag
make fixture        # regenerate the dry-run video
```

Git: feature branch, PR, never push `main` directly. Releases get a plain-English
title and a `## Summary` written for a non-technical reader.

Design: the plan this was built from (`longform-video-pipeline-plan.md` in the author's
dev folder) records every decision (D1–D27) and the ticket order.
