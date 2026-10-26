# Post manifest

`<job>/manifest.json` is the closed-loop ledger of every (clip × channel)
schedule call made for one long-form video, with the scheduler's response kept
verbatim so a later fetcher can join platform-native analytics back to the
source. It is the Go rewrite (`internal/manifest`) of
`claude-social-media-skills/_shared/post-manifest/post_manifest.sh` (D24); the
shape is the vendored one plus the fields marked *new*. Every write is atomic
(temp file + rename via `model.WriteFileAtomic`).

## Shape

```jsonc
{
  "schema_version": "1",                      // new
  "project_id": "P3051823ab0w",               // OpusClip project
  "source_video": {
    "id": "uEposKmbFvY", "title": "How to Scale Without the Slop",
    "url": "https://www.youtube.com/watch?v=uEposKmbFvY",
    "duration_seconds": 1840,                 // new
    "profile": "evc"                          // new
  },
  "created_at": "2026-05-18T23:36:43Z",
  "clips": [
    {
      "clip_id": "La4Wghg6IX",
      "title": "...", "description": "... [opus:La4Wghg6IX]",
      "score": 99, "duration_sec": 28, "theme": "...",
      "status": "post",                       // new: post | held | expired
      "published_url": "...",                 // new, optional
      "curated_at": "2026-05-18T23:00:00Z",   // new, optional; expire's clock
      "scheduled_posts": [
        {
          "clip_id": "La4Wghg6IX",            // new (redundant with the parent, kept for flat consumers)
          "project_id": "P3051823ab0w",       // new: the cell's project, so monitor can read the schedule back
          "label": "FACEBOOK_PAGE Enterprise Vibe Code",
          "platform": "facebook",             // new
          "account_id": "6946d924a163ebb2222f505b",
          "sub_account_id": "913906768469231",
          "transport": "buffer",              // new, optional: absent = OpusClip; "buffer" for a static
          "scheduled_at_utc": "2026-05-19T16:00:00Z",
          "api_response": { "data": { "scheduleId": "1779159097402OmCL-FACEBOOK_PAGE", "hasConflict": false } },
          "schedule_id": "1779159097402OmCL-FACEBOOK_PAGE",   // new: the line's post_id, else api_response.data.scheduleId, else the line's schedule_id
          "error": "429 rate limited",        // new, only when the schedule call failed
          "check": {                          // new: the last `longform monitor` verdict
            "checked_at": "2026-05-19T00:10:00Z",
            "method": "opusclip-rest",        // or manifest-only (Buffer)
            "status": "pending",              // pending | not_pending | missing | replaced | manifest_only
            "note": "...",                    // optional
            "response": { "scheduleId": "1779159097402OmCL-FACEBOOK_PAGE", "platform": "FACEBOOK_PAGE", "publishAt": "2026-05-19T16:00:00.000Z", "...": "..." }
          }
        }
      ]
    }
  ]
}
```

`scheduled_posts` stays nested under each clip so `pm-tool lint/backfill` and
`content-attribution` in the social repo keep reading it unchanged. A non-JSON
scheduler body is stored as `{"raw": "<body>"}`.

## Subcommands

All take `--job <jobdir>` (or `--manifest <path>` to name the file directly).

| command | effect | stdout |
|---|---|---|
| `init [--project ID] [--source-video ID] [--source-title T] [--source-url U] [--duration-seconds N] [--profile P] [--force]` | create the manifest; idempotent unless `--force` | — |
| `ensure-clip --clip-id ID [--title T] [--description D] [--score N] [--duration-sec N] [--theme S] [--status post\|held] [--curated-at RFC3339]` | add a clip entry if missing; existing entries are untouched | — |
| `append-post [--file lines.jsonl]` | read the JSON lines `longform opus schedule` emits (`{"cell": <cell>, "schedule_id": "...", "response": <verbatim>, "error": "..."}`) from stdin or `--file`; ensure each clip from its cell; append one scheduled post per line with the response verbatim, the cell's `project_id`, and `schedule_id` = the line's `post_id`, else the line's `schedule_id` (not when `error` is set), else `response.data.scheduleId`. A Buffer static's line adds `"transport":"buffer","post_id":"<Buffer post id>"`: the Buffer reply shape is not wired into the binary, so the skill names the id. Idempotent per id: a line whose id is already recorded (in the manifest or earlier in the same input) is not appended again, stderr `append-post: duplicate <id> ignored`, exit 0; a line without an id (a failed call) is always appended. The whole input is read before the manifest is loaded: `opus schedule --job` records each post in the manifest itself as it is made (`manifest.RecordLine`, same append logic), so its piped lines come back as duplicates | — |
| `count-scheduled` | total (clip × channel) records (`0` when no manifest) | integer |
| `find-clip --clip-id ID` | one clip's full record; exit 65 when absent | JSON object |
| `conflicts` | posts whose `api_response.data.hasConflict` is true (LinkedIn reports a structural false positive because its org page and person profile share one `postAccountId`; treat LinkedIn conflicts as advisory) | JSON array (with `clip_title`) |
| `schedule-ids` | every schedule id, one per line; feeds batch-cancel loops | lines |
| `list-by-channel <label substring>` | posts whose label contains the substring (case-insensitive) | JSON array (with `clip_title`) |
| `expire [--days N] [--now RFC3339]` | mark held clips older than N days as `expired`, persist, print them; without `--days` N is the job profile's `backlog_days` (needs `--job`) | JSON array |
| `seal [--allow-missing] [--now RFC3339]` | `approved -> scheduled` once every approved cell has a recorded post; adds the backlog's clips as `held`; exit 65 lists missing cells (see `cli-contract.md`) | result JSON, or verdict lines |

Held clips enter the manifest at `seal` (the draft's `held` clips and the
rows `approve except` held) or through `ensure-clip --status held`. `expire`
dates a held clip by `clips[].curated_at`, else the job's
`clips.json` `curated_at`/`created_at`, else `job.json` `created_at`, else the
manifest's `created_at`.

Exit codes: `0` ok, `64` usage, `65` data (missing clip, unparseable line,
clip not found), `70` internal (write failure).

## In-text tag convention

Every scheduled description still carries the `[opus:<clip_id>]` footer tag
(last line, after a blank line) so the source link survives without the
manifest. See the vendored README for the scheme table.
