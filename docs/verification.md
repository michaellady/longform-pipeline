# Verification

How to prove the `longform` binary and the plugin work, what a green run looks like, and what the harness does not cover. The agent-facing version of this page is the `longform-verify` skill (`plugins/longform-pipeline/skills/longform-verify/SKILL.md`); this page is the human one.

## What exists

| Piece | Path | Purpose |
|---|---|---|
| Fake OpusClip API | `internal/fakeopus` | In-memory stand-in for `https://api.opus.pro/api` plus the GCS resumable upload. Realistic bodies, configurable processing delay, 429 injection, a request log with redacted headers. Used by tests and by the wizard's dry run. |
| Standalone fake | `longform fakeopus --addr 127.0.0.1:0` | Starts the fake, prints the API URL (export it as `OPUSCLIP_API_URL`, with `OPUSCLIP_API_KEY=test`), serves until Ctrl-C. Flags: `--delay`, `--duration-sec`, `--api-key`, `--quiet`. |
| End-to-end test | `internal/e2e/e2e_test.go` | Gated by `LONGFORM_E2E=1`. Builds the binary, sandboxes every directory, generates a fixture with ffmpeg, drives the full CLI contract against the fake, asserts state and artifacts after each step. |
| Verify skill | `plugins/longform-pipeline/skills/longform-verify/SKILL.md` | Feature map (feature, proving command, artifact, done condition per ticket), launch, doctor, drive, evidence, cleanup. |

## Running everything

```bash
# Unit level: must be green before anything else. The e2e test skips here.
go build ./... && go vet ./... && go test ./...

# Fake server alone
go test ./internal/fakeopus/ -v

# End to end (needs ffmpeg; ~40 s with the 2 s fake processing delay and the 1 req/s schedule pacing)
LONGFORM_E2E=1 go test ./internal/e2e/ -run TestPipeline -v

# Keep the sandbox to inspect the job folder afterwards
LONGFORM_E2E=1 LONGFORM_E2E_KEEP=1 go test ./internal/e2e/ -run TestPipeline -v

# Run real transcription instead of the LONGFORM_SKIP_TRANSCRIBE=1 shortcut
LONGFORM_E2E=1 LONGFORM_WHISPER_MODEL=~/.config/longform-pipeline/models/ggml-medium.en-q5_0.bin \
  go test ./internal/e2e/ -run TestPipeline -v

# Standalone fake for a wizard dry run
longform fakeopus --addr 127.0.0.1:0 --delay 5s
```

The e2e test never touches `~/.config/longform-pipeline`, `~/Library/Application Support/longform-pipeline`, or the network: `LONGFORM_CONFIG_DIR`, `LONGFORM_DATA_DIR`, `LONGFORM_LOG_DIR` and `HOME` all point into a temp root, and `OPUSCLIP_API_URL` points at the fake.

## What the e2e test asserts

1. `stage --profile evc --file <fixture>`: one job under `<data>/jobs/` in state `staged` with `sources/ analysis/ review/ statics/ logs/`, one source with a sha256. A second run creates no second job.
2. `prepare <job>`: state `prepared`; `delivery.mp4`, `analysis/transcript.json`, `analysis/censored.json` exist. Re-run is a no-op.
3. `upload <job>`: state `uploaded`; both project ids in `job.json`; the fake saw exactly two `POST /clip-projects` (one with `curationPref.clipDurations`, one with `curationPref.skipCurate: true`) and one `PUT` of the bytes. Re-run submits nothing.
4. `wait <job> --interval 1s --ceiling 30s`: state `clipped`; `clips.json` carries both project ids, `full_clip_id`, and the fake's three shorts clips with exactly one `is_bonus`.
5. The test writes `review/draft.json` (the skill's output) referencing those clip ids: two clips `post` (scores 91 and 76), one `held` (64); cells on youtube and instagram; the full video on youtube.
6. `brief render --job`: state `drafted`, `review/brief.md` written.
7. `brief parse-decision --job "approve"`: state `approved`; `review/decision.json` and `review/schedule.json` with one cell per posted clip cell plus the full video.
8. `slots propose`: every cell has an RFC3339 `publish_at` (accepts stdout JSON or an in-place rewrite).
9. `gate validate`: exit 0.
10. `opus schedule --cells`: one JSON line per cell with `response.data.scheduleId`; the fake saw one `POST /publish-schedules` per cell paced at no faster than 1 req/s.
11. `manifest init` then `manifest append-post --file <jsonl>`: `manifest.json` has one `scheduled_posts` entry per cell with a `schedule_id` the fake actually issued.
12. `manifest seal --job`: state `scheduled`, `extra.scheduled_at` set, the draft's one held clip (score 64) entered the manifest as `held`; a second run is a no-op.
13. `watch --once --profile evc --stable-seconds 0` (the real driver, with `LONGFORM_CLAUDE` pointed at `true` so its `launch-session` starts nothing): it runs `monitor`, which reads every schedule back with `GET /publish-schedules?q=findByProjectAndClip` (state `monitored`, `extra.monitor.method` `opusclip-rest`, `pending` equal to the cell count, every post's `check.status` `pending` with the fake's entry verbatim in `check.response`), then `manifest expire` and `close` (both exit 0; `logs/monitor.log`, `expire.log`, `close.log` exist). `monitor` again is a no-op.
14. Backlog, with the clock moved 26 days ahead through `--now` (the profile's `backlog_days` is 25): `close` exits 0 but leaves the job `monitored` while the held clip is unexpired ("1 held clips"); `manifest expire --now` expires it; `close --now` moves the job to `closed` with `extra.closed_at` equal to that time.
15. `status --json` lists the job.
16. The fake never saw an `Authorization` value other than `Bearer test` on any `/api` request, the schedule reads included.

Every subprocess failure is reported with the command line, exit code, stdout and stderr. A subcommand the binary does not know yet (exit 64, `unknown command`) turns into a `t.Skip` naming the command, so the test is informative before the subcommand branches merge.

## Expected output

A green run prints one `$ longform <args> -> exit 0` line per step, the fake's request log lines (`fakeopus: POST /api/clip-projects -> 201` and so on), and ends with:

```
    e2e_test.go:NNN: pipeline complete: job 2026-10-21-evc-<hash8> reached closed with 4 scheduled posts; fake served NN requests
--- PASS: TestPipeline (12x.xxs)
PASS
ok      github.com/michaellady/longform-pipeline/internal/e2e   4x.xxxs
```

Until every ticket merges, the run stops at the first missing subcommand. Output of the harness on this branch before the merge:

```
$ LONGFORM_E2E=1 go test ./internal/e2e/ -run TestPipeline -v
=== RUN   TestPipeline
    e2e_test.go:319: LONGFORM_WHISPER_MODEL unset: running prepare with LONGFORM_SKIP_TRANSCRIBE=1 (transcript will be empty)
    e2e_test.go:324: $ longform version -> exit 0
    e2e_test.go:327: $ longform stage --profile evc --file .../inbox/IMG_0001.mp4 -> exit 64
    e2e_test.go:327: STOPPED at `longform stage --profile evc --file .../inbox/IMG_0001.mp4`: the binary does not implement it yet (exit 64, unknown command).
        longform stage --profile evc --file .../inbox/IMG_0001.mp4
        exit=64
        --- stdout ---

        --- stderr ---
        longform: unknown command "stage"

        Usage: longform <command> [flags]

          fakeopus         Run the fake OpusClip API server for dry runs and tests
          status           List jobs and their states (--json for machine output)
          version          Print the binary version
--- SKIP: TestPipeline (0.86s)
PASS
ok      github.com/michaellady/longform-pipeline/internal/e2e   1.576s
```

## Fake server fidelity

Shapes mirror `references/api-reference.md` and the jq paths in the `opusclip` CLI script (2.2.5):

- `POST /upload-links` → `{url, uploadId, dnsUrl, useAmount, totalAmount}`; `POST <url>` with `x-goog-resumable: start` → 201 with `Location`; `PUT <Location>` stores the byte count and reads the MP4 `mvhd` duration.
- `POST /clip-projects` → 201 `{projectId}`. A shorts project yields three clips (28 s / 55 s / 88 s, scores 91 / 76 / 64, the last a bonus clip). A full project (`skipCurate`) yields one clip whose `durationMs` comes from, in order: a `<n>s` token in `uploadedVideoAttr.title`, the uploaded MP4's `mvhd`, or `--duration-sec` / `Options.DefaultDurationSec`.
- `GET /exportable-clips?q=findByProjectId&projectId=P` → `{data: []}` until the processing delay elapses, then `{data: [{id: "P.C", curationId, rank, score, title, description, hashtags, durationMs, isBonusClip, uriForPreview, uriForExport, uriForThumbnail, text, clipKeywords, judgeResult{hookScore,…}, renderPref{layoutType, layoutAspectRatio}, renderAsVideoPreview{pending}, renderAsVideoFile{pending}}]}`.
- `GET /exportable-clips/P.C?include=editingScript` → the clip plus `editingScript.tracks[]` (`KeyFrameTrack`, `CaptionTrack` with per-word `textElements`, `EmojiTrack`, `TextOverlayTrack`).
- `POST /exportable-clips/P.C/re-render` → `{jobId}`; the clip reports `renderAsVideoFile.pending: true` for one processing delay.
- `POST /publish-schedules` → 201 `{data: {scheduleId}}`; 400 when `projectId`, `clipId`, `postAccountId`, `publishAt` (RFC3339) or `postDetail` is missing. `DELETE /publish-schedules/ID` → `{data: ID}`.
- `GET /publish-schedules?q=findByProjectAndClip&projectId=P[&clipId=C]` → `{data: [{scheduleId, extUserId, extOrgId, platform, postDetail{title, custom, thumbnailUrl}, publishAt}]}` and `?q=byPage` → `{data: {data: [...], meta: {page, nextPage, total}}}`, the shapes the 2026-10-25 probe read from the real API. Like the real list it holds only pending schedules: one whose `publishAt` has passed is treated as posted and drops out; `DropSchedule(id)` removes one early. Any other `q` → 400 naming the two supported values; `GET /publish-schedules/ID` → 404 `Cannot GET` (no read by id on the real API either).
- `GET /social-accounts?q=mine` → four accounts including `acc-yt` (YOUTUBE) and `acc-ig` (INSTAGRAM_BUSINESS, sub `ig-sub-1`); `GET /brand-templates?q=mine` → two templates.
- Every `/api` request without exactly `Authorization: Bearer <key>` → 401. `Inject429(n)` makes the next n `/api` requests return 429 with `Retry-After: 1`.
- The request log keeps method, path, query, headers (Authorization replaced by `<redacted>`), body (PUT bodies as `<N bytes>`), status, and `auth_matched`.

## Known gaps

- Transcription runs only when `LONGFORM_WHISPER_MODEL` is set; otherwise `prepare` runs with `LONGFORM_SKIP_TRANSCRIBE=1` and the censor pass has no words to match. The censor list format and the skip switch follow the `prepare` contract as stated by its author (`{"schema_version":"1","words":[...]}`; `LONGFORM_SKIP_TRANSCRIBE=1`).
- Not exercised: HDR tonemapping, `vidstab`, BJJ stitching, the `strip_overlay` re-render pass (the fake supports it; the profile in the test has it off), Buffer statics (and so `manifest_only` checks and seal's Buffer `post_id` path, which unit tests in `internal/lifecycle` cover), Slack and macOS notifications, native YouTube posting, `/yt-launch`.
- The `monitored` check and backlog expiry run against the fake only. Live, `longform monitor` was run once (2026-10-25) on a scratch copy of a real job against the real API: a real pending scheduleId read back `pending` (and `overdue`: a February schedule that never fired), a made-up one with a future time failed with exit 70. Never exercised live: a schedule this pipeline created being read back, the transition of a real post from `pending` to gone after it publishes (the fake models it from `publishAt`), and anything about whether a post actually went out, which the REST key cannot see (the claude.ai connector's `opusclip_list_scheduled_posts` can).
- `stage` waits its default 60-second stability window in the e2e run (twice), which is most of the test's two minutes.
- On `/exportable-clips` the fake implements `findByProjectId` only; `findByCollectionId`, collections, censor jobs and social-copy jobs return 404.
- The harness codes against the CLI contract in the plan, not against the subcommand implementations, which were written in parallel. Where the contract left a choice (`slots propose` output on stdout vs in place; `manifest append-post` reading the `opus schedule` JSON lines verbatim) the test accepts the documented alternatives; anything else is a real contract mismatch and should be fixed in the subcommand, not papered over here.
- The 1 req/s pacing assertion tolerates 900 ms between schedule calls to allow for scheduler jitter.
