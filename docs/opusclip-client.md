# OpusClip client and commands

`internal/opusclip` is the typed REST client; `longform upload`, `longform wait` and
`longform opus <sub>` are the commands built on it. Everything here is transport
(Primitive Test: atomic state writes, rate limits, retries); no editorial judgment.

## Configuration

| Env | Meaning |
|---|---|
| `OPUSCLIP_API_KEY` | Bearer token (Pro or Enterprise). Required. Never logged: the client only ever puts it in the `Authorization` header of calls to the API host, never to the upload (GCS) host, and `RedactingLogger` scrubs any `Bearer …` / `Authorization: …` / `OPUSCLIP_API_KEY=…` text that reaches a log line. |
| `OPUSCLIP_API_URL` | API root, default `https://api.opus.pro/api`. Tests point it at `httptest`. |

Client behaviour:

- 30 req/min token bucket on every API call; a second 1 req/s pacer on `POST /publish-schedules`, `DELETE /publish-schedules/{id}`, `POST /post-tasks`, `POST /social-copy-jobs`.
- 429 and 5xx (and transport errors) retry with exponential backoff + jitter (`MaxRetries` 4, base 1 s, cap 30 s). 4xx other than 429 never retry.
- Non-2xx responses become `*opusclip.APIError{Status, Method, Path, Body}` (body capped at 512 bytes); `IsStatus(err, 404)` and `ae.Temporary()` are the two helpers.
- `context.Context` on every call; `longform` commands cancel on SIGINT/SIGTERM.

## Live facts the code depends on (probed 2026-10-21, see `docs/probes/` on the probes branch)

| Fact | Where it is handled |
|---|---|
| `POST /clip-projects` returns top-level `id`/`projectId`, `stage: "QUEUED"`, `title`, `sourceInfo.durationMs`, and echoes `curationPref`/`renderPref`. `clipDurations [[0,30],[0,60],[0,180]]` is accepted verbatim. | `Project{ProjectID, Stage, Title, SourceDurationMs}`; `Project.CreditEstimate()` = ceil(source minutes). |
| A `skipCurate` project yields exactly one clip, `durationMs` = whole source, title `<upload title>_OpusClip Captions`, `rank/score/description/hashtags: null`; its clip id is a 10-char hash only readable from the list endpoint. | `wait` takes `full_clip_id` from the first clip of the full project; `Clip` tolerates every null. The gate must always send its own title. |
| A curation project's clip count grows while it is still processing (first clip at +237 s); the list endpoint carries no project stage. | `wait` requires two consecutive polls with the same non-zero count on both projects before it proceeds. If a project-status endpoint is confirmed later, swap it in at `waitJob`. |
| `hashtags` is one space-separated string on curated clips (`"#A #B"`), null on skipCurate clips. | `opusclip.StringList` decodes null / string / array; `clips.json` always has an array. |
| `renderAsVideoFile` and `renderAsVideoPreview` are `null` on a clip that has never been re-rendered. | Ready predicate is `Clip.RenderReady()` = `renderAsVideoFile == null || pending != true`. Never test `.pending == false`. |
| The describe transcript (`text`) is one string with literal `__silence` tokens and no timestamps. | `analysis/opus-transcripts.json` has `transcript` verbatim and `transcript_clean` with the tokens dropped. Whisper stays the primary transcript. |
| `uriForExport` / `uriForPreview` are signed GCS URLs that expire. | `clips.json` carries `fetched_at` and `uris_expire: true`; `longform opus export` re-fetches fresh URIs at approval time. Never put them in a manifest as durable. |
| There is **no REST usage endpoint**: `GET /usage`, `/credits`, `/me`, `/user` all 404. `POST /upload-links` returns `useAmount`/`totalAmount` = upload **storage** bytes (100 GiB cap), not credits. | `longform opus usage` reports the watcher's own estimate from `job.json` (`extra.opusclip_projects[].credits_estimate`, summed); `--storage` additionally reads the storage quota (creates one unused upload link). The true balance is only in the dashboard or the OAuth MCP `opusclip_get_usage`. |
| (Probed 2026-10-25, `docs/probes/2026-10-25-publish-schedules-read.json`.) No schedule read is documented and `GET /publish-schedules/{id}` is a route miss (404 `Cannot GET`), but the 400 for an unknown `q` names two list modes: `q=findByProjectAndClip&projectId=P[&clipId=C]` → `{data: [{scheduleId, extUserId, extOrgId, platform, postDetail{title, custom, thumbnailUrl}, publishAt}]}` and `q=byPage` → `{data: {data, meta{page, nextPage, total}}}`. Both list only schedules still pending (status `scheduled` in the connector, even months past `publishAt`); a schedule that posted is gone, and no status, post URL or failure reason is returned. | `Client.ClipSchedules(ctx, project, clip)` reads the per-clip list (30 req/min bucket, not paced); `longform monitor` uses it once per clip. `byPage` is not used: one call returned all 73 entries while `nextPage` was set, so its paging is unclear. |
| Facebook and LinkedIn share one `postAccountId` across sub-accounts. | `ScheduleRequest.SubAccountID` is sent whenever present; `opus schedule --profile` resolves `sub_accounts[platform]` too. |
| The only brand template (`665fefb18ce49592eaeff8a0`) already has `screenOverlays: []`. | `StripOverlay` describes the clip first and returns `ErrNoOverlay` (no re-render) unless the payload shows an overlay (`screenOverlays` non-empty, `screenOverlay`/`screenOverlayFileId` non-null, or `enableScreenOverlay: true` anywhere in `renderPref`/`editingScript`). `--force` overrides. |

## Upload (4 steps) and `longform upload <jobdir>`

1. `POST /upload-links` `{"video":{"usecase":"LocalUpload"}}` → `{url, uploadId}`. `job.opusclip.upload_id` is saved **immediately**, with the signed url in `extra.opusclip_upload_url`.
2. `POST <url>` with `x-goog-resumable: start`, `Content-Length: 0` → `Location` (saved to `extra.opusclip_upload_session`). No `Authorization` header goes to this host.
3. `PUT <Location>` with `Content-Type: application/octet-stream`, streamed from disk (`*os.File` body, never read into memory). On a dropped connection or 5xx the client queries the session with `Content-Range: bytes */<size>`, reads the committed offset from the `308` `Range` header, and sends only the tail with `Content-Range: bytes <off>-<size-1>/<size>`. `extra.opusclip_upload_done` is set when the PUT completes.
4. `POST /clip-projects` twice with `videoUrl = uploadId`:
   - **shorts**: `curationPref{model, clipDurations [[0,d]…] from profile.opusclip.shorts.durations, customPrompt (ClipAnything only)}`, `renderPref{layoutAspectRatio: shorts.aspect, quickstartConfig.enableRemoveFillerWords}`, `brandTemplateId = profile.opusclip.template_id`, `uploadedVideoAttr.title = job.slug`.
   - **full**: `curationPref{skipCurate: true}`, `renderPref{layoutAspectRatio: profile.opusclip.full.aspect}`, same template, title `<slug> (full)`.

   Each project id is written to `job.json` the moment it exists, together with `extra.opusclip_projects[]` (`id, kind, stage, title, source_duration_ms, credits_estimate, created_at`) and `extra.opusclip_credits_estimate`.

Re-runs: a job with `upload_id` skips step 1; with a session and no `upload_done` it resumes step 3; with one project id it creates only the missing one; at `uploaded` with both ids it exits 0 without any API call. State: `prepared → uploaded`. Exit codes: 0, 64 usage, 65 bad job/profile/key, 70 API failure, 75 temporary (429/5xx/timeout/interrupt). A per-job `upload.lock` prevents two concurrent runs.

## `longform wait <jobdir> [--interval 270s] [--ceiling 2h]`

Polls `GET /exportable-clips?q=findByProjectId&projectId=…` for both projects until both counts are ≥1 and unchanged across two consecutive polls. Then, in order:

1. If `profile.opusclip.strip_overlay` and `job.opusclip.overlay_stripped` is false: for every clip of **both** projects, `GET /exportable-clips/<P>.<C>?include=editingScript`; skip when no overlay is present; otherwise `POST /exportable-clips/<P>.<C>/re-render` with `{editingScript, renderPreferenceOverride:{screenOverlays:[], screenOverlay:null, screenOverlayFileId:null, enableScreenOverlay:false, disableScreenOverlay:true}}`, 3 s apart. `overlay_stripped` is persisted after the pass so a later ceiling never re-submits. Then every clip is polled (15 s) until `RenderReady`, and the lists are re-fetched.
2. Write `clips.json`:

   ```json
   {"schema_version":"1","shorts_project_id":"…","full_project_id":"…","full_clip_id":"…",
    "fetched_at":"2026-…Z","uris_expire":true,
    "clips":[{"project_id":"…","clip_id":"…","rank":1,"title":"…","description":"…","hashtags":["#a"],
              "score":87,"duration_sec":42,"uri_preview":"…","uri_export":"…","uri_thumbnail":"…",
              "is_bonus":false,"is_full":false,"hook_score":7,"coherence_score":7,"connection_score":6,"trend_score":7,
              "raw":{…verbatim clip object…}}]}
   ```
3. Write `analysis/opus-transcripts.json` (`{schema_version, clips:[{project_id, clip_id, title, transcript, transcript_clean}]}`) from the describe endpoint.
4. Set `artifacts.clips`, `artifacts.opus_transcripts`, `opusclip.full_clip_id`; advance `uploaded → clipped`.

Ceiling reached (or SIGINT) → exit 75 and the state is unchanged; nothing partial is written except the persisted `overlay_stripped` flag.

## `longform opus <subcommand>`

| Subcommand | Call | Output |
|---|---|---|
| `accounts` | `GET /social-accounts?q=mine` | verbatim JSON |
| `templates` | `GET /brand-templates?q=mine` | verbatim JSON |
| `usage [--storage]` | local `job.json` scan (+ `POST /upload-links` for storage) | `{note, credits_estimate, jobs[], storage?}` |
| `describe --project P --clip C [--include editingScript]` | `GET /exportable-clips/P.C` | verbatim clip |
| `export --project P --clip C [--out file]` | `GET /exportable-clips/P.C` (+ download) | `{uri_export, uri_preview, render_pending, …}` |
| `strip-overlay --project P --clip C [--wait] [--force]` | describe + re-render | re-render response, or `{skipped:true}` |
| `schedule --cells cells.json [--profile NAME] [--dry-run]` | `POST /publish-schedules` per cell at 1 req/s | one JSON line per cell: `{cell, schedule_id, response, error, status}` |
| `unschedule <scheduleId>` | `DELETE /publish-schedules/{id}` | verbatim JSON |

`longform monitor <jobdir>` (see `cli-contract.md`) is the only caller of
`GET /publish-schedules?q=findByProjectAndClip`; there is no `opus` subcommand
for it.

Cells: `[{project_id, clip_id, platform, account_id, sub_account_id?, title, description?, privacy?, media_type?, publish_at}]`. `publish_at` is any RFC 3339 time and is normalised to UTC `…Z`. With `--profile`, an empty `account_id` resolves through `profile.opusclip.accounts[platform]` (and `sub_accounts[platform]`). A failing cell (local validation or API error) is reported on its line and the run continues; exit is 65 if any cell failed, else 0.

## Verification

- `go build ./... && go vet ./... && go test ./...` — `httptest` only, no real API.
- One real smoke call, `longform opus accounts`, was run on 2026-10-21 from the login shell (key in `OPUSCLIP_API_KEY`); only platform/name fields were recorded in the build report.
