---
name: longform-verify
description: "Prove the longform-pipeline works end to end: build the `longform` binary, drive one job from a dropped fixture file to a populated manifest against the fake OpusClip server, and inspect the artifact that proves each feature. Use for /longform-pipeline:longform-verify, \"verify the pipeline\", \"does the longform binary work\", before tagging a release, after merging a ticket, or when a stage command misbehaves. Never spends OpusClip credits."
---

# Verify the longform pipeline

You are reading this cold, mid-task, to prove that the pipeline works. Everything below runs from the repo root (`longform-pipeline`, the checkout that holds `cmd/longform`). No step touches the real OpusClip API, Buffer, Slack, or the user's real profiles: every run gets its own `LONGFORM_CONFIG_DIR`, `LONGFORM_DATA_DIR`, and `LONGFORM_LOG_DIR`, and `OPUSCLIP_API_URL` points at the in-process fake (`internal/fakeopus`).

## Launch

The surface is a CLI, so launch means build once, then run each drive in a fresh sandbox.

```bash
go build -o /tmp/longform-verify/longform ./cmd/longform   # or: make build
/tmp/longform-verify/longform version                     # prints "longform dev" (or the tag)
```

Ready means `longform help` lists every stage command in the feature map below. A missing name means that ticket's branch is not merged yet; `mustOK` in the e2e test turns that into a skip that names the command.

External tools the real path needs: `ffmpeg` and `ffprobe` on PATH (`brew install ffmpeg`), `whisper-cli` plus a ggml model for transcription. Without a model, export `LONGFORM_SKIP_TRANSCRIBE=1` and `prepare` writes an empty transcript and still reaches `prepared` (it records `extra.prepare.transcribe_skipped` in `job.json`). Say so in the evidence when you take that shortcut: it proves everything except the whisper call.

## Doctor

One read-only check before driving anything:

```bash
go build ./... && go vet ./... && go test ./...     # unit tests, e2e skips without LONGFORM_E2E=1
which ffmpeg ffprobe whisper-cli
/tmp/longform-verify/longform help                 # stage prepare upload wait brief slots gate opus manifest status fakeopus
```

If `go test ./...` is red, stop: do not drive a binary whose units fail.

## Drive

### The whole pipeline in one command (preferred)

```bash
LONGFORM_E2E=1 go test ./internal/e2e/ -run TestPipeline -v
```

`internal/e2e/e2e_test.go` builds the binary, writes a minimal `evc` profile and censor list into temp dirs, renders a 5-second 1080x1920 fixture with ffmpeg, starts the fake OpusClip server with a 2-second processing delay, and runs the CLI contract in order, asserting `job.json` state after every step:

```
stage --profile evc --file <fixture>      -> staged      (second run: still one job)
prepare <job>                             -> prepared    (delivery.mp4, analysis/transcript.json, analysis/censored.json)
upload <job>                              -> uploaded    (2 POST /clip-projects, 1 PUT of bytes, re-run submits nothing)
wait <job> --interval 1s --ceiling 30s    -> clipped     (clips.json: 3 shorts clips, 1 bonus, full_clip_id)
<test writes review/draft.json referencing the fake clip ids>
brief render --job <job>                  -> drafted     (review/brief.md)
brief parse-decision --job <job> approve  -> approved    (review/decision.json, review/schedule.json)
slots propose --profile evc --cells ...   -> every cell has an RFC3339 publish_at
gate validate --profile evc --cells ...   -> exit 0
opus schedule --cells ...                 -> one JSON line per cell with response.data.scheduleId, paced 1 req/s
manifest init / append-post               -> manifest.json scheduled_posts: one per cell, each schedule_id issued by the fake
status --json                             -> lists the job
```

It ends by asserting the fake never saw an `Authorization` value other than `Bearer test`. Every subprocess failure prints the command, exit code, stdout and stderr. `LONGFORM_E2E_KEEP=1` keeps the temp root and logs its path so you can inspect the job folder afterwards.

### Driving one stage by hand

When a single command is suspect, reproduce the e2e sandbox and run only that stage:

```bash
export ROOT=$(mktemp -d) LONGFORM_CONFIG_DIR=$ROOT/config LONGFORM_DATA_DIR=$ROOT/data LONGFORM_LOG_DIR=$ROOT/logs
export OPUSCLIP_API_KEY=test
export OPUSCLIP_API_URL=$(/tmp/longform-verify/longform fakeopus --addr 127.0.0.1:0 --quiet & sleep 0.3; echo "$!" > $ROOT/fake.pid; wait) # prints http://127.0.0.1:PORT/api
```

Simpler: run `longform fakeopus --addr 127.0.0.1:0` in its own terminal, copy the URL it prints, and export it. Then write a profile at `$LONGFORM_CONFIG_DIR/profiles/evc.json` (copy the one the e2e test writes: `writeProfileAndCensor` in `internal/e2e/e2e_test.go`), a censor list at `$LONGFORM_CONFIG_DIR/censor/default.json` (`{"schema_version":"1","words":[...]}`), generate the fixture:

```bash
ffmpeg -y -f lavfi -i testsrc=size=1080x1920:rate=30 -f lavfi -i sine=frequency=440 -t 5 \
  -c:v libx264 -preset ultrafast -pix_fmt yuv420p -c:a aac -shortest $ROOT/IMG_0001.mp4
```

and run the stage commands from the table above one at a time, reading `job.json` after each.

### Wizard dry run

`/longform-pipeline:longform-setup` ends with a dry run on the bundled fixture. The dry run is the same drive with `OPUSCLIP_API_URL` set to a `longform fakeopus` instance and `OPUSCLIP_API_KEY=test`; it must reach `clipped` (ticket 0's done condition). The fake's `--delay` flag (e.g. `--delay 5s`) makes `wait` actually poll; `--duration-sec` sets the full clip's length when the uploaded MP4 has no readable `mvhd`.

## Feature map

Each row: the feature, the command that proves it, the artifact to inspect, and what "verified" means for the ticket in the plan (section 8).

| Ticket | Feature | Proving command | Artifact to inspect | Verified when |
|---|---|---|---|---|
| 1 | CLI scaffold, profiles | `longform status --json`; `go test ./internal/model/` | stdout JSON | every registered command appears in `longform help`; an invalid profile fails `Validate` |
| 2 | Stage (dedupe by hash) | `longform stage --profile evc --file F` twice | `<data>/jobs/<id>/job.json`, `jobs.json` | state `staged`, one source with sha256, second run creates no job |
| 2 | Prepare (remux, censor, transcript) | `longform prepare <job>` | `delivery.mp4`, `analysis/transcript.json`, `analysis/censored.json` | state `prepared`; re-run is a no-op; with a real model the censor list's words appear in `censored.json` |
| 3 | Upload (4-step, two projects) | `longform upload <job>` | `job.json` `.opusclip`; fake request log | both project ids recorded; exactly 2 `POST /clip-projects` (one `clipDurations`, one `skipCurate:true`); one `PUT`; re-run submits nothing |
| 3 | Wait / clips summary | `longform wait <job> --interval 1s --ceiling 30s` | `clips.json` | state `clipped`; 3 shorts clips, exactly one `is_bonus`, `full_clip_id` set; `/exportable-clips` was polled |
| 3 | Overlay strip | same, with `opusclip.strip_overlay: true` in the profile | fake log: `GET ...?include=editingScript`, `POST .../re-render` | one re-render per shorts clip; `job.json` `.opusclip.overlay_stripped` true |
| 5 | Brief | `longform brief render --job <job>` | `review/brief.md` | state `drafted`; brief lists every clip and the full video from `draft.json` |
| 5 | Approval grammar | `longform brief parse-decision --job <job> "approve"` | `review/decision.json`, `review/schedule.json` | state `approved`; one cell per posted clip cell plus one per full-video platform |
| 4 | Slots | `longform slots propose --profile evc --cells C --existing E` | cells file / stdout | every cell has an RFC3339 `publish_at` within cadence |
| 4 | Gates | `longform gate validate --profile evc --cells C` | exit code | 0 for a clean set; 65 with a named reason for an over-length title or a profane caption |
| 6 | Schedule | `longform opus schedule --cells C` | stdout JSON lines; fake log | one line per cell with `response.data.scheduleId`; calls paced at 1 req/s; never a non-`Bearer test` auth value |
| 6 | Manifest | `longform manifest init`, `manifest append-post --file R` | `manifest.json` | `scheduled_posts` has one entry per cell with the verbatim response and a `schedule_id` the fake issued |
| 0 | Wizard dry run | `/longform-pipeline:longform-setup` with `OPUSCLIP_API_URL` = fake | `job.json` | job reaches `clipped` on the bundled fixture |
| 9 | Fake fidelity | `go test ./internal/fakeopus/` | test output | upload, project, clip, editing-script, re-render, schedule, 401 and 429 paths all pass |

Not covered by the harness (see docs/verification.md "Known gaps"): HDR tonemapping, vidstab, BJJ stitching, real whisper output, Buffer statics, Slack and macOS notifications, YouTube native posting, the `monitored` tick.

## Evidence

A proof is the `-v` output of the e2e test (or the hand-driven transcript) plus the job folder. Capture:

- the command list with exit codes (`$ longform ... -> exit N` lines),
- `job.json` after the last step (`state`, `opusclip`, `artifacts`, `history`),
- `clips.json`, `review/schedule.json`, `manifest.json`,
- the fake's request log when the question is about traffic (the e2e test prints it on failure; `h.fake.Requests()` in a scratch test prints it on demand).

Proof standards: drive the real binary, not package functions; assert the state file and the artifact, not only the exit code; the fake is the only mock and it sits at the production HTTP boundary. A skipped transcription is a stated gap, never a silent pass.

Evidence goes in `$LONGFORM_E2E_KEEP` root (printed at the top of the run) or, for a hand drive, in `$ROOT`. Copy what you need out before cleanup.

## Cleanup

- `t.TempDir()` removes everything the e2e run created unless `LONGFORM_E2E_KEEP=1`; remove that root yourself when done.
- Kill the `longform fakeopus` you started (`kill $(cat $ROOT/fake.pid)` or Ctrl-C in its terminal). Never `pkill longform`: a launchd watcher may be running for the user's real profiles.
- Never delete anything under `~/.config/longform-pipeline` or `~/Library/Application Support/longform-pipeline`; the harness never writes there.

## Helpers

- `internal/e2e/e2e_test.go`: the harness; `newHarness` is the sandbox recipe, `mustOK` the skip-on-unknown-command rule.
- `internal/fakeopus`: the fake server (`fakeopus.New`, `Start`, `Requests`, `UnexpectedAuth`, `Inject429`).
- `longform fakeopus --addr 127.0.0.1:0 [--delay 5s] [--duration-sec N] [--api-key K] [--quiet]`: the standalone fake; prints the API URL on stdout and serves until SIGINT.
