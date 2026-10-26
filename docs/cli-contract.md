# `longform` CLI contract

Every subsystem registers its subcommands in `internal/cli/cmd_<name>.go`
through `cli.Register`. This document is the contract between those
subsystems: what the watcher expects of each job-stage command and what the
ingest subsystem (`watch`, `stage`, `launch-session`, `notify`) guarantees to
the others.

## Shared rules

Verbatim from the build brief:

> Every job-stage command takes the job dir as its only positional arg, is
> idempotent, exits 0 on success and non-zero otherwise; stage commands
> advance `job.State` themselves.

Exit codes follow `internal/cli/registry.go` (sysexits): `0` ok, `64` usage,
`65` data/validation failure, `70` internal error, `75` temporary failure
(retry on the next pass). A command that cannot make progress yet, for
example `wait` while OpusClip is still rendering, exits `75` and leaves the
state untouched; the watcher logs it and tries again next pass.

Paths come from `internal/model/paths.go` and are overridable for tests and
the wizard dry run:

| Env var | Default | Holds |
|---|---|---|
| `LONGFORM_CONFIG_DIR` | `~/.config/longform-pipeline` | `profiles/<name>.json`, templates, corpora |
| `LONGFORM_DATA_DIR` | `~/Library/Application Support/longform-pipeline` | `jobs/<id>/`, `jobs/jobs.json` (dedupe index), `watch.lock`, `photos/<profile>.json` and `photos/incoming/` (Photos album feed) |
| `LONGFORM_LOG_DIR` | `~/Library/Logs/longform-pipeline` | `<id>.session.json`, `<id>.session.err`, launchd stdout/stderr |

## Job directory layout after `staged`

```
<DATA>/jobs/<YYYY-MM-DD>-<profile>-<hash8>/
├── job.json              owned by the binary (model.Job)
├── sources/01-<orig>     every original, moved (never copied-and-left, never deleted)
├── sources/02-<orig>     ... in recording order for stitch jobs
├── stitched.mp4          stitch jobs only: lossless concat, or re-encode
├── analysis/ review/ statics/   empty until later stages
└── logs/
    ├── events.log        one line per notify call: <RFC3339>\t<event>\t<message>
    └── <cmd>.log         stdout+stderr of every `longform <cmd> <dir>` the watcher ran
```

`job.json` fields the ingest subsystem writes (all others are left zero):

- `sources[]`: `id` (first 12 hex of the sha256), `path` (job-relative,
  `sources/NN-<original basename>`), `original` (inbox path it came from),
  `sha256`, `size_bytes`, `recorded_at` (ffprobe `creation_time`, falling back
  to mtime, UTC), `probe`.
- `probe`: `duration_ms`, `width`, `height`, `fps` (decimal string, at most
  three decimals: `"30"`, `"29.97"`), `codec` (`h264`, `hevc`), `audio_codec`,
  `color_transfer`, `rotation` (ffprobe display-matrix rotation as reported,
  e.g. `-90`; `0` when absent), `hdr` (`color_transfer` in
  `{smpte2084, arib-std-b67}`). `probe` is omitted only when ffprobe is not
  installed.
- `content_hash`: sha256 of the sorted source sha256s joined by `"\n"`.
  For a single source this is **not** the file hash.
- `stitched`: `true` when more than one source was concatenated.
- `extra.stitched_path` (`"stitched.mp4"`), `extra.stitched_mode`
  (`"copy"` or `"reencode"`), `extra.stitched_duration_ms`.
- `extra.session_launched_at` (RFC3339), `extra.session_model`,
  `extra.session_pid`, `extra.session_stdout`, `extra.session_stderr`:
  written by `launch-session`.
- `history[]`: `stage` appends `detected -> staged`; a failure mid-stage
  appends `-> failed` with the error in `note` and `error`.

**Input for `prepare`:** `ingest.InputPath(job)` returns `<job>/stitched.mp4`
when `job.stitched` is true, otherwise `<job>/<sources[0].path>`. Other
packages should use that helper rather than re-deriving the rule.

**Dedupe index** (`<DATA>/jobs/jobs.json`): every source sha256 and the
content hash map to the job id. A file whose hash is already indexed is
skipped and left in the inbox (it is never deleted); the log names the job
it duplicates.

## `longform watch`

```
longform watch [--profile <name>|all] [--once] [--stable-seconds 60] [--interval 10m]
```

Brief, verbatim:

> for each profile (model.ListProfiles), scan profile.Inbox; for each
> candidate run the stage logic; then for every job not yet `clipped` exec
> in order `longform prepare <dir>`, `longform upload <dir>`,
> `longform wait <dir>` (same executable, os.Executable()), stopping at the
> first non-zero exit and logging to job logs/; when a job reaches `clipped`
> exec `longform launch-session <dir>` once (record `extra.session_launched_at`
> in job.json so it never launches twice). One job in flight per profile:
> skip starting a new job's upload while another job of that profile is
> between `uploaded` and `clipped`. `--once` runs one pass and exits
> (launchd WatchPaths mode); without it, loop every 10 minutes. Take a
> model.Lock named "watch" under model.DataRoot() so two launchd fires never
> overlap.

Details:

- A step runs only when the job is in the state that precedes it
  (`staged -> prepare`, `prepared -> upload`, `uploaded -> wait`), so an
  already-advanced job is not re-exec'd. After a step exits 0 the watcher
  reloads `job.json`; if the state did not advance it stops that job for the
  pass and logs it. A non-zero exit stops the job for the pass; nothing is
  marked failed by the watcher (the stage command owns that decision).
- After a profile's inbox stage, a profile with `photos.enabled` also gets
  one Photos album pass (the same pass as `longform photos-watch --once`,
  below). Its errors, such as osxphotos missing or no Full Disk Access, are
  logged as `watch: photos <profile>: <error>` and never stop the inbox or the
  job steps; a pass that saw recordings logs its counts. launchd's
  `WatchPaths` only covers the inbox folders, so album recordings are picked
  up on the 10-minute `StartInterval` tick (or any inbox event).
- Jobs are processed oldest first (`created_at`, then id). "In flight"
  means another job of the same profile is in state `uploaded`.
- Jobs in `failed`, `closed` or `detected` (staging crashed midway) are left
  alone. A clipped-or-later job without `extra.session_launched_at` gets
  `launch-session` once per pass until it succeeds. `clipped` and `drafted`
  jobs wait for the editorial session and the human approval.
- Past approval (see "Post-approval" below), in the same pass:
  - `approved`: not driven. The approval phase stopped before
    `manifest seal`; the watcher logs
    `watch: job <id> is approved but not sealed; re-run the approval phase, resuming at longform manifest seal --job <dir> (it lists the cells still to post)`
    once per pass.
  - `scheduled`: `longform monitor <dir>` (log `logs/monitor.log`). Exit 0
    moves it to `monitored`; a non-zero exit stops the job for the pass
    (`75` is retried next pass; `70` means a schedule did not resolve and is
    retried every pass until someone fixes it).
  - `monitored` (including a job monitor just advanced):
    `longform manifest expire --job <dir>` (log `logs/expire.log`), then, if
    it exited 0, `longform close <dir>` (log `logs/close.log`). Close exits 0
    without advancing for as long as the backlog runs (normally
    `profile.backlog_days`, 25 days); its reason is in `close.log`, and the
    watcher's own log gets a line only when the job closes.
- Per-step output goes to `<job>/logs/<name>.log` (`<name>` is the command,
  `expire` for `manifest expire`) with `=== <ts> longform <command line>`
  and `=== exit <n>` markers; watcher progress goes to stderr (launchd's
  `StandardErrorPath`).
- `LONGFORM_SELF` overrides the executable the watcher execs (tests point
  it at a stub). The exec'd command inherits the watcher's environment, so
  launchd must set `PATH` (ffmpeg at `/opt/homebrew/bin`), `OPUSCLIP_API_KEY`
  and `LONGFORM_SLACK_WEBHOOK` in `EnvironmentVariables`; no login shell is
  involved. The launchd plist should also set `AbandonProcessGroup` to true
  so the detached claude session outlives the `--once` run (the session is
  started in its own session id regardless).
- Lock held: `<DATA>/watch.lock`. A second invocation exits `75` without
  doing anything; a stale lock from a dead pid is taken over.
- Exit `0` after a pass even when individual jobs failed a step; `70` when
  profiles or jobs could not be listed; `64` on bad flags.

## `longform stage`

```
longform stage --profile <name> [--file <path>...] [--stable-seconds 60] [--json]
```

One inbox pass for one profile (used by tests and the wizard dry run);
`--file` replaces the inbox scan with explicit files, which are **moved**
into the job like any inbox file (copy a bundled fixture first). Prints
`staged <id> <dir>` per job created, or the job list as JSON with `--json`.
Exit `65` when the profile is missing or invalid, `70` on an I/O failure.

Stage logic, verbatim from the brief, then details:

> candidates are regular files with video extensions (.mov .mp4 .m4v .MOV
> .MP4); call `brctl download <path>` and wait for the file to be
> materialised (no placeholder; `brctl` may be missing in tests, treat as
> no-op); require size and mtime unchanged for --stable-seconds (poll every
> 5 s; in --once mode a file that is still changing is simply skipped this
> pass); sha256 each file; skip hashes present in model.LoadIndex(); if
> profile.Stitch.Enabled group files whose recording time (ffprobe format
> tag creation_time, fallback mtime) falls within profile.Stitch.SessionWindowMin
> of each other, ordered by recording time, and losslessly concatenate with
> the ffmpeg concat demuxer and `-c copy` into one file, falling back to a
> re-encode (libx264 crf 18, aac) when `-c copy` fails; content hash of a
> job = sha256 of the sorted source hashes joined by newline; create the job
> with model.NewJob; MOVE each original into <job>/sources/ (os.Rename,
> fallback copy+remove across volumes) and write Source records with Probe
> filled from ffprobe (duration, width, height, fps, codec, audio codec,
> color_transfer, rotation, HDR = color_transfer in {smpte2084, arib-std-b67});
> add the hash to the Index; advance to `staged`; call notify.

- Extensions are matched case-insensitively. Dot-files are ignored except
  iCloud placeholders `.<name>.icloud`, which trigger `brctl download` on
  the real name; the real file is then watched for the stability window.
  `brctl download` is only invoked for placeholders and for paths under
  `Library/Mobile Documents/` (iCloud Drive).
- Stability: all candidates are polled together, so one pass waits one
  window, not one per file. In `--once` mode (always on for `stage`) a file
  that changes, or a placeholder that has not materialised by the end of the
  window, is skipped this pass. In loop mode the window restarts on change
  and a file is given up after an hour.
- For a stitch profile, if any candidate was skipped the whole pass is
  deferred, because a batch of class segments arrives over several seconds
  and grouping the early ones would split the session into two jobs.
- Grouping chains consecutive files whose recording times are within the
  window of each other (A..B 60 min, B..C 60 min with a 90-minute window
  is one session).
- Stitch: `-c copy` through the concat demuxer, validated by probing the
  output (duration must be within max(1 s, 2 %) of the input sum). Inputs
  that differ in codec, geometry, fps, rotation, audio codec or colour
  transfer go straight to the re-encode, which normalises to the first
  input's displayed geometry and fps through the `concat` filter (libx264
  crf 18, aac 192k). A single-file group is not stitched.
- Files ffprobe rejects are skipped (not a job); if ffprobe is absent the
  job is staged without a probe and the stitch falls back to `-c copy` only.
- A failure after the job directory exists (move, stitch, index) marks the
  job `failed` with the error and notifies `failed`; the moved sources stay
  in `sources/`.
- `notify` is called with event `staged` and message `<n> source(s)[, stitched (<mode>)]`.
- Stage output is deterministic for a given inbox: candidates sorted by
  path, groups by recording time, source order inside a group by recording
  time then path.

## `longform photos-watch`

```
longform photos-watch --profile <name> [--once] [--interval 5m] [--dry-run] [--osxphotos <path>]
```

The second feed, run on its own: the phone adds a recording to the profile's
Apple Photos album (`photos.album`), iCloud Photos syncs the bytes, and this
command lists the album with `osxphotos` and stages every recording whose
original is on this Mac. `watch` runs the same pass for every profile with
`photos.enabled`, so launchd needs nothing extra; this command is for checking
a profile by hand (`--dry-run`), for one pass (`--once`), or for a foreground
loop every `--interval` (default `5m`) until SIGINT/SIGTERM.

One pass:

- `osxphotos query --album <album> --only-movies --json --mute`
  (`--library <photos.library>` when set). Only movies are considered; each is
  keyed by its Photos UUID.
- osxphotos is `--osxphotos`, else `photos.osxphotos` (an explicit path is
  used as given), else the first that exists of `$LONGFORM_OSXPHOTOS`, `PATH`,
  `~/.local/bin/osxphotos`, `/opt/homebrew/bin/osxphotos`,
  `/usr/local/bin/osxphotos`.
- A recording is ready when osxphotos reports a `path` and that file exists
  (the database's `ismissing` flag lags a download, so the file is the
  truth). A ready original is cloned (APFS `clonefile`, falling back to a
  verified byte copy) to
  `<DATA>/photos/incoming/<profile>/<uuid>/<original filename>`. All of a
  pass's ready clones go to the stage logic in one batch, exactly like
  `stage --file`: moved into `sources/`, hashed, probed, deduped against the
  index, grouped and stitched by recording time for a stitch profile,
  `staged`, notified. `extra.photos` on the job is a list of asset maps in
  source order, one per recording in the job (a stitched job has several),
  each with `uuid`, `album`, `original_path`, `original_filename`, `date`.
  The Photos library is only read, never changed.
- Not ready: with `photos.download_missing` the pass first runs
  `osxphotos export <incoming>/<profile>/<uuid> --uuid <uuid> --download-missing --use-photokit --skip-edited --no-progress`
  (needs Photos access for the calling process). Still not ready, the asset
  stays `waiting` and, the first time only, `notify` fires event `waiting`
  with `Waiting for <file> (<size>) to finish syncing from iCloud Photos to this Mac`.
- Stitch profiles: while any album recording is still `waiting`, the pass
  stages nothing (mirroring the inbox's deferral while files are still
  arriving), so a class's clips are grouped into one job once all of them
  are on this Mac. The deferral shows as a `deferred` count in the `watch`
  and `photos-watch` summary lines.
- A clone failure leaves the asset `waiting` for the next pass.
- A per-file rejection by stage (not a video, unreadable video, hash error,
  stage group failed) marks that asset `failed` at once with stage's reason
  in `note`, keeps its clone under `incoming/` for inspection, and fires one
  `failed` notification: `Could not stage <file> from the Photos album: <reason>`.
- A stage error that stops the whole batch (index unreadable, etc.) is
  retried: the assets stay `waiting` with note `stage attempt n/3: <err>`, and
  only the third failed attempt marks them `failed` (one notification each).
- Failed assets are never retried automatically; delete the asset's entry
  from the state file to retry.
- Only a true duplicate, whose hash is already in the index, is treated as
  done: the asset is `fed` with no `job_id`, a note, and the clone removed.
- A `fed` asset is never fed again, even if it stays in the album; removing
  it from the album changes nothing.

State, written atomically after every pass: `<DATA>/photos/<profile>.json`:

```json
{
  "schema_version": "1",
  "album": "EVC",
  "assets": {
    "<uuid>": {
      "uuid": "<uuid>", "original_filename": "IMG_0420.MOV", "size_bytes": 3400000000,
      "status": "waiting | fed | failed",
      "first_seen": "<RFC3339>", "last_seen": "<RFC3339>", "fed_at": "<RFC3339>",
      "job_id": "<id>", "incoming": "<path>", "note": "<text>", "notified": true, "attempts": 0
    }
  }
}
```

`--dry-run` lists the album next to the feed state, `UUID FILE SIZE ORIGINAL
FEED JOB` on stdout (`ORIGINAL`: `on disk`, `waiting`, `not in iCloud yet`;
`FEED`: `new` or the recorded status), with a summary line on stderr. It takes
no lock and stages, downloads, notifies and writes nothing; the setup wizard
runs it after installing launchd as the access check. Remember that macOS
attributes a terminal-run command's Full Disk Access to the terminal app,
while the LaunchAgent's run uses the `longform` binary's own grant.

Exit codes: `0` pass done (or the loop stopped by a signal), also when single
assets failed, since those are recorded in the state; `64` bad flags or no
`--profile`; `65` profile missing or invalid, or it has no album feed
(`photos.enabled` false or `photos.album` empty); `69` osxphotos not found or
it cannot read the Photos library (grant Full Disk Access to the process that
runs it), in loop mode too, with osxphotos' own crash log written to
`osxphotos_crash.log` under the log dir (`~/Library/Logs/longform-pipeline`,
or `LONGFORM_LOG_DIR`); `70` any other failure with `--once` or
`--dry-run` (without `--once` it is logged and retried next interval); `75`
the `watch` lock is held (a `watch` pass is running). Progress goes to stderr.

## `longform shortcut`

```
longform shortcut --profile <name> [--brand <display>] [--kind send|mark] [--album <name>] [--open]
```

Builds and signs the iPhone Share-sheet shortcut for one profile and prints
the signed file's path on stdout. `--kind` picks which:

| `--kind` | Shortcut name | What it does on the phone |
|---|---|---|
| `send` (default) | `Send to <Brand> inbox` | saves the shared video into `iCloud Drive/Shortcuts/longform-inbox/<profile>/` (the inbox feed) |
| `mark` | `Mark for <Brand>` | adds the shared video to the Photos album `--album` (the album feed; nothing is copied) |

- `--brand` defaults to the profile's `brand.display`, else the profile name.
- `--album` is used only with `--kind mark` and defaults to the profile's
  `photos.album`; when both are empty the command exits `64`.
- Output: `<CONFIG>/shortcuts/<Name>.shortcut` (signed), with the unsigned
  plist beside it as `<Name>.unsigned.shortcut`.
- `--open` hands the signed file to the Shortcuts app, which shows an Add
  dialog; one click adds it and iCloud syncs it to the phone.
- Exit `0` ok, `64` bad flags, an unknown `--kind`, or `mark` without an
  album, `70` when building or signing fails (the unsigned file's path is
  printed on stderr). The recipe for building either by hand is
  `docs/shortcut-recipe.md`.

## `longform launch-session`

```
longform launch-session [--force] [--claude <path>] [--model <name>] <jobdir>
```

Brief, verbatim:

> runs `claude -p "/longform-pipeline:longform-publish <jobdir>" --permission-mode acceptEdits
> --output-format json` with stdout to model.LogRoot()/<jobid>.session.json
> and stderr to .session.err, non-blocking (start and return), recording pid
> and start time in job.extra.

Details: refuses (`65`) unless the job is at least `clipped`, prints and
exits `0` without launching when `extra.session_launched_at` is already set;
`--force` overrides both. The claude binary is `--claude`, then
`$LONGFORM_CLAUDE`, then `PATH`, then `~/.local/bin/claude` (`70` when none
is found). The session runs on `--model`, then `$LONGFORM_CLAUDE_MODEL`,
then `opus` (the claude CLI alias for the current Opus); the value is passed
as `claude --model <name>` and recorded in `extra.session_model`. The
process starts with the job directory as its working
directory (so `acceptEdits` covers `review/` writes) and in its own session
id so launchd does not reap it with the watcher. The log files are opened in
append mode, so a `--force` relaunch adds to them.

## `longform notify`

```
longform notify --job <jobdir> --event <string> [--message <text>]
```

Brief, verbatim:

> macOS notification via `osascript -e 'display notification ...'` when
> profile.Notify.MacOS; Slack via incoming webhook URL from env
> LONGFORM_SLACK_WEBHOOK when set (POST JSON {text}); never fail the caller
> if notifications fail. Export a Go function notify.Send(profile, job,
> event, message) for other packages.

Details: always appends `<job>/logs/events.log` first. The Slack text is
`[longform <profile>] <event>: <job id>: <message>` (the job id is prefixed
when the message does not already contain it). The macOS notification has
title `longform <profile>`, subtitle `<event>`, body the message. Failures
are printed to stderr and the command still exits `0`; only bad flags (`64`)
or an unreadable job (`65`) are non-zero. The profile is loaded by
`job.profile`; when it is missing only Slack and the events log fire.
`LONGFORM_OSASCRIPT` overrides the osascript binary for tests. The webhook
URL is a secret and is never printed.

`notify.Send(p *model.Profile, j *model.Job, event, message string) error`
accepts nil for either argument and returns the joined channel errors for
logging only.

## Post-approval: `manifest seal`, `monitor`, `close`

The approval phase of the `longform-publish` skill schedules every approved
cell (OpusClip clips and full video through `longform opus schedule`,
statics through Buffer) and appends each response to `manifest.json`. These
three commands then move the job through the last states, in
`internal/lifecycle`. All three take the job dir as `--job <dir>` or as one
positional argument, and `--now <RFC3339>` to override the clock (tests).

### `longform manifest seal`

```
longform manifest seal --job <jobdir> [--allow-missing] [--now RFC3339]
```

Run by the skill as the last approval step, before `notify --event scheduled`.

- Requires `approved`. A job already `scheduled` or later: message on stderr,
  exit `0`, nothing changes. Any other state (or `failed`): exit `65`.
- Reads `review/schedule.json` (the cells `brief parse-decision` approved,
  `job.artifacts.schedule`) and `manifest.json`. Every cell that is not
  parked (`status` `held` or `skipped`) must match one recorded post: same
  `clip_id` and platform, a non-empty `schedule_id` and no `error`. For an
  OpusClip cell that id is the `scheduleId` from `opus schedule`; for a
  static it is the Buffer post id the skill wrote as `post_id` on the
  `append-post` line (`"transport":"buffer"`). Each post satisfies one cell
  only, so a doubled append never hides a missing cell.
- Missing cells and no `--allow-missing`: one verdict object per line on
  stdout, in the gate's style, then exit `65`; nothing is written:

  ```json
  {"kind":"static","clip_id":"s1","label":"static s1 (quote)","platform":"instagram","field":"post","reason":"no post recorded in manifest.json","fix_hint":"post it through Buffer, then append ..."}
  ```

  `reason` is `no post recorded in manifest.json`, `the schedule call
  failed: <error>`, or `a post is recorded without an id` (a Buffer line
  without `post_id`).
- With `--allow-missing` the job is sealed anyway and the verdicts go to
  `job.extra.seal.missing`.
- On success: the clips that stay in the backlog are added to the manifest
  with `status: "held"` (draft clips with `status: held`, rows held by
  `approve except`, clips whose only cells are held; never a `drop`ped row,
  never a clip that posted), so `manifest expire` and `close` can count them
  down. A missing `manifest.json` is created. `job.extra.seal` =
  `{sealed_at, cells, posts, held_clips[, missing]}`,
  `job.extra.scheduled_at` = now (RFC 3339 UTC), state `approved ->
  scheduled` with note `<n> posts sealed` (plus `; <m> cells missing,
  allowed by --allow-missing`). The result JSON (`cells`, `posts`,
  `missing`, `held_clips`, `advanced`) is printed on stdout.
- `notify` is not called; the skill notifies separately (`notify` never
  moves state).
- Exit: `0`, `64` usage, `65` missing cells / wrong state / unreadable job
  or schedule, `70` write failure.

### `longform monitor`

```
longform monitor [--now RFC3339] (--job <jobdir> | <jobdir>)
```

Run by the watcher for every `scheduled` job.

- Requires `scheduled`; `monitored` or later: message, exit `0`. Earlier
  states, `failed`, or no `manifest.json`: exit `65`.
- Every OpusClip post (`transport` empty or `opusclip`) with an id is read
  back with `GET /publish-schedules?q=findByProjectAndClip&projectId=P&clipId=C`,
  once per clip. That endpoint is undocumented: OpusClip names it in the 400
  for an unknown `q` (probe:
  `docs/probes/2026-10-25-publish-schedules-read.json`). There is no read by
  id. The list holds only schedules still pending; a schedule that posted
  leaves it, and the API has no status, post URL or failure reason. The
  project id comes from the post's `project_id`, else `review/schedule.json`,
  else `clips.json`.
- Each post gets `check` in `manifest.json`:
  `{checked_at, method, status, note?, response?}` where `response` is the
  scheduler's list entry verbatim. `status`:

  | status | meaning | counts as |
  |---|---|---|
  | `pending` | listed by OpusClip | verified (a `note` flags one still pending over an hour after its publish time: it has not gone out) |
  | `not_pending` | not listed, and its `scheduled_at_utc` has passed | not verified: posted, failed or cancelled; the REST key cannot tell |
  | `missing` | not listed, publish time still ahead | failure |
  | `replaced` | not listed, but a later post for the same clip and platform is `pending` (the cell was re-scheduled and its new line appended) | not a failure |
  | `manifest_only` | a Buffer post (`transport: buffer`) | id present only; nothing is read back |

  Posts with an `error` or no id are skipped (seal already accounted for
  them); an id appended twice is checked once (`duplicate_ids`).
- Failures: one verdict per line on stdout (`schedule_id`, `clip_id`,
  `platform`, `label`, `field`, `reason`, `fix_hint`), `job.extra.monitor`
  records them under `failures`, the state stays `scheduled`, exit `70`.
  `notify` fires event `monitor_failed` when the set of failing ids differs
  from the previous run's, so a watcher retrying every pass notifies once.
- Success: `job.extra.monitor` = `{method, checked_at, posts, pending,
  not_pending, manifest_only[, overdue]}` (`method` is `opusclip-rest`, or
  `manifest-only` when the job has no OpusClip posts), `job.extra.monitored_at`,
  state `scheduled -> monitored` with a note naming what was and was not
  verified, the result JSON on stdout.
- Exit: `0`, `64` usage, `65` wrong state / no manifest / no
  `OPUSCLIP_API_KEY` while OpusClip posts need reading, `70` a schedule did
  not resolve or a non-retryable API error, `75` a 429/5xx/network failure
  (the watcher retries next pass).

What the REST key cannot verify: whether a post actually went out, and
anything about Buffer posts. The human check is the claude.ai OpusClip
connector's `opusclip_list_scheduled_posts` (status `scheduled`, `posted` or
`failed`, `post_url`, `failure_reason`) and the Buffer queue.

### `longform manifest expire` (watcher use)

`longform manifest expire --job <dir>` without `--days` takes the job
profile's `backlog_days` (default 25). It marks every `held` clip whose
curation time (`curated_at`, else `clips.json`, else `job.json` `created_at`)
is more than that many days old as `expired` and prints them. `--days N`
(0 or more) overrides; `--days` is required with `--manifest` and no `--job`.

### `longform close`

```
longform close [--now RFC3339] (--job <jobdir> | <jobdir>)
```

Run by the watcher after `manifest expire` for every `monitored` job.

- Requires `monitored`; `closed`: message, exit `0`. Other states: `65`.
- The backlog ends at `extra.scheduled_at` (else the history entry that
  reached `scheduled`) plus the profile's `backlog_days`. Before that, or
  while any manifest clip is still `held`, it changes nothing, prints
  `{state, held, expired, backlog_ends, closed:false}` and logs
  `close: <id> stays monitored: <n> held clips, backlog ends <date>`; exit `0`.
- Otherwise `job.extra.closed_at` = now and `monitored -> closed` with note
  `backlog ended <date>; <n> held clips expired`.
- Exit: `0`, `64` usage, `65` wrong state / no profile / no manifest / no
  scheduled time, `70` write failure.

## What the other job-stage commands must honour

- `prepare <dir>`: input is `ingest.InputPath(job)`; advance `staged -> prepared`;
  re-running on a `prepared` or later job is a no-op exit `0`.
- `upload <dir>`: advance `prepared -> uploaded`; record both project ids
  before anything else so a re-run never re-submits.
- `wait <dir>`: advance `uploaded -> clipped` when both renders are done;
  exit `75` and leave the state alone while they are not.
- `monitor <dir>`: advance `scheduled -> monitored` only when every OpusClip
  schedule that should still be pending is; exit `75` on a retryable API
  failure; never mark the job `failed` for a missing schedule (exit `70`).
- `close <dir>`: advance `monitored -> closed` only when the backlog is over;
  exit `0` and leave the state otherwise.
- Any stage may mark the job `failed` (`job.Advance(model.StateFailed, by, note)`);
  the watcher then leaves it alone until a human resets it.
- All of them may call `notify.Send` for their own events; the watcher does
  not notify on their behalf.
