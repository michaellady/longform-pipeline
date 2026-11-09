# `review/draft.json` — the skill-to-binary contract

The `longform-publish` skill writes `<job>/review/draft.json` once it has made
every editorial choice for a job. The `longform` binary only reads it:

- `longform brief render` lays it out as `review/brief.md` (plan D21 section order).
- `longform brief parse-decision` combines it with your approval grammar into
  `review/decision.json` and the derived `review/schedule.json` cells.
- `longform brief render-statics` fills the profile templates from `statics[]`
  (engine `templates`); `longform brief render-carousel` (engine
  `newsletter-carousel`) and `longform brief render-html` (engine
  `custom-html`) render statics in the draft phase instead.
- `longform gate statics` checks each static's `visual_review` against its
  caption and image bytes.

Go types: `internal/brief/draft.go`. Paths are relative to the job directory
unless they are URLs. Times are RFC3339 in UTC; the brief shows them in the
profile's timezone.

```json
{
  "schema_version": "1",
  "job_id": "2026-10-21-evc-abcdef01",
  "profile": "evc",
  "full_video": {
    "project_id": "...", "clip_id": "...",
    "title": "...", "description": "...",
    "chapters": [{"at_ms": 0, "title": "Intro"}],
    "thumbnails": [{"path": "review/thumb-1.jpg", "chosen": true}],
    "platforms": ["youtube", "instagram", "facebook", "linkedin_page", "linkedin_profile", "tiktok"],
    "captions": {"facebook": "..."}
  },
  "clips": [{
    "project_id": "...", "clip_id": "...",
    "score": 82, "duration_sec": 58, "short_title": "...",
    "status": "post",
    "preview_url": "...",
    "cells": [{
      "platform": "youtube", "account_id": "...", "sub_account_id": "",
      "title": "...", "description": "...", "hashtags": ["#a"],
      "publish_at": "2026-10-23T16:00:00Z"
    }]
  }],
  "statics": [{
    "id": "s1",
    "format": "quote",
    "platforms": ["instagram", "linkedin_page"],
    "caption": "...",
    "inputs": {
      "quote": "...", "attribution": "...",
      "timestamp_ms": 123000,
      "slides": [{"kind": "hook", "text": "...", "sub": "...", "timestamp_ms": 0,
                  "fields": {"LINE_1": "...", "LINE_2": "...", "TEASE": "..."},
                  "scene": "...", "illustration": "statics/s1/illus/slide-01.png"}],
      "pages": ["statics/s1/html/page-01.html"]
    },
    "visual_review": {
      "reviewer": "fresh image-capable subagent", "caption": "<exact caption>",
      "verdict": "PASS", "evidence": "...", "source_excerpt": "...",
      "images": [{"path": "statics/s1/slide-01.png", "sha256": "...", "url": "https://raw.githubusercontent.com/.../slide-01.png"}],
      "reviewed_at": "2026-11-08T20:00:00Z"
    }
  }],
  "censored": [{"word": "...", "start_ms": 1, "end_ms": 2}],
  "stabilization": {"before": "review/stabilize-before.jpg", "after": "review/stabilize-after.jpg"},
  "credits": {"spent": 32, "to_spend": 6},
  "decisions": [{"id": "d1", "question": "...", "options": ["..."], "default": "..."}]
}
```

## Fields

| Field | Meaning |
|---|---|
| `schema_version` | Required. `"1"`. |
| `job_id`, `profile` | Echo of `job.json`; the binary trusts `job.json`. |
| `full_video.project_id` / `clip_id` | The `skipCurate` OpusClip project and its single clip. |
| `full_video.chapters[].at_ms` | Chapter start; rendered as `mm:ss`. |
| `full_video.thumbnails[]` | Candidates in display order; `swap thumbnail <n>` picks by 1-based position. Exactly one `chosen`. |
| `full_video.platforms` | Surfaces the full video posts to. Account ids come from the profile (`opusclip.accounts`, `opusclip.sub_accounts`). |
| `full_video.captions` | Optional post text per non-YouTube platform. `title`/`description` are the YouTube package. A Facebook full cell gets `title` = `full_video.title` and `description` = `captions.facebook` (else `description`); another platform listed here posts its caption as `title`. |
| `clips[].cells[]` on `facebook` | Write `title` = `short_title`, `description` = the full caption. parse-decision enforces it either way: a Facebook cell always leaves with `title` = the clip's `short_title` and `description` = the caption (the cell's `description`, else its `title`). |
| `clips[].status` | `post` (fills a cadence slot) or `held` (backlog). Empty means `post`. Anything else is rejected. |
| `clips[].cells[]` | One per platform. `publish_at` is the slot `longform slots propose` returned; empty means not slotted. |
| `clips[].cells[].hashtags` | Includes the leading `#`. |
| `statics[].format` | Engine `templates`: `quote`, `carousel`, `still`, `frame_carousel`. Engine `newsletter-carousel`: `carousel`, `quote`. Engine `custom-html`: any name the profile lists in `statics.formats`. |
| `statics[].inputs.slides[].fields` / `scene` / `illustration` | Engine `newsletter-carousel`: `fields` fill the carousel-newsletter template placeholders for the slide's `kind` (hook `LINE_1 LINE_2 TEASE`; section `KICKER HEADLINE_1 HEADLINE_ACCENT BODY`; quote `QUOTE ATTRIBUTION`, `QUOTE` defaults to `text`; stat `STAT_NUMBER STAT_UNIT STAT_LABEL_1 STAT_LABEL_2`; cta `HEADLINE_1 ACCENT_WORD BODY`, `ACCENT_WORD` defaults to and must be `newsletter`). `scene` is the illustration prompt; `illustration` the PNG (default `statics/<id>/illus/slide-NN.png`). |
| `statics[].inputs.pages` | Engine `custom-html`: the HTML files the skill designed, one per image, in order. |
| `statics[].visual_review` | The image-caption alignment record (below). |
| `statics[].platforms` | Buffer channels to queue to. `linkedin_*` platforms receive the PDF for carousels. |
| `statics[].inputs.quote` / `attribution` | Quote card text. |
| `statics[].inputs.timestamp_ms` | Frame for a `still`. |
| `statics[].inputs.slides[]` | Carousel panels; `kind` is `hook`, `section`, `quote`, `stat` or `cta`. `frame_carousel` slides also carry `timestamp_ms` for their frame. |
| `censored[]` | Spans beeped in `delivery.mp4`; shown as `mm:ss–mm:ss`. |
| `stabilization` | Before/after frame pair, or both empty when stabilization was off. |
| `credits` | OpusClip credits already spent on this job, and still to spend when approved. |
| `decisions[]` | Questions the skill leaves to you. `decide <id> <option>` answers one; unanswered ones take `default`. |

## `visual_review`

An independent, image-capable reviewer (a fresh subagent that opens the PNGs)
inspected every final image of the static beside its exact caption and the
source transcript passage. `longform gate statics` blocks the static when the
record is missing, its `verdict` is not `PASS`, `reviewer` or `evidence` is
empty, `caption` differs from `statics[].caption` (or from the caption in
`review/schedule.json` once approved), an image was added, removed or its
bytes changed (`sha256`), or, with `--hosted`, the bytes at `url` differ. Any
caption or image change therefore invalidates the review. One record covers
every platform of the static because the caption is the same on all of them;
split a static whose captions must differ. `longform gate statics --list`
prints the paths and digests to copy into `images`.

## Clip row numbers

The brief's clip table numbers rows 1..N. The order is deterministic and
derived from the draft alone so `approve except 3,7` means the same clip in
`render` and `parse-decision`: earliest `publish_at` first (unslotted clips
last), then score descending, then `clip_id`. Held clips appear in the table
with status `hold`.

## Outputs

`review/decision.json`:

```json
{
  "schema_version": "1",
  "grammar": "approve except 3; hold linkedin",
  "approved": true,
  "held_clips": [3],
  "dropped_clips": [],
  "held_platforms": ["linkedin"],
  "thumbnail": 1,
  "decisions": {"d1": "Hook A"},
  "note": "",
  "at": "2026-10-21T17:00:00Z"
}
```

`review/schedule.json` is `{"schema_version":"1","job_id":"...","cells":[...]}`
where each cell is the shape the gates read:
`kind` (`clip` | `full` | `static`), `clip_id`, `project_id`, `platform`,
`account_id`, `sub_account_id`, `title`, `description`, `hashtags`,
`duration_sec`, `publish_at`, `label`. Only surviving cells are listed:

- `full`: one per `full_video.platforms` entry not held; `publish_at` is the
  next occurrence of the profile's first local slot after approval (D13).
- `clip`: every cell of every `post` clip that was neither held nor dropped,
  minus held platforms.
- `static`: one per static per platform; `account_id` is the Buffer channel
  (`buffer.channels[platform]`, falling back to the family key such as
  `linkedin`); `publish_at` is empty because statics go to the Buffer queue.

## Approval grammar

Commands joined by `;` (or `,` where unambiguous), case-insensitive:

| Command | Effect |
|---|---|
| `approve` | Everything as drafted. |
| `approve except 3,7` | Rows 3 and 7 become held. |
| `hold linkedin` | Drop every cell on that platform family (`linkedin` covers `linkedin_page` and `linkedin_profile`). Must match a platform in the draft. |
| `drop 4` | Row 4 is removed entirely. |
| `swap thumbnail 2` | Choose thumbnail 2. |
| `decide d1 Hook B` | Answer a decision; the option text or its 1-based number. |
| `reject <note>` | Not approved; the job returns to `drafted` with the note in its history. |

Any other token is an error naming it (exit 65).
