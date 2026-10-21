# YouTube upload (`longform yt-upload`)

`longform yt-upload` pushes a finished video to YouTube through the Data API
v3 with title, description, chapters, tags, category, a custom thumbnail,
privacy, and an optional scheduled publish time. Per plan decision D8 it is
the fallback for the native OpusClip YouTube post, used only when that post
cannot carry chapters and a custom thumbnail.

It is pure transport: it validates what it is handed and sends it. What the
title says is decided upstream, behind the human gate (D6).

## One-time setup

1. In Google Cloud Console, enable **YouTube Data API v3** on a project and
   create an **OAuth 2.0 Client ID** of type **Desktop app** (APIs & Services
   -> Credentials). The client the `youtube-analytics` tool already uses works;
   both are installed-app clients.
2. Download the client JSON and save it as
   `~/.config/longform-pipeline/youtube/client_secret.json`
   (or point `LONGFORM_YT_CLIENT_SECRET` at the file; `~` is expanded).
   `LONGFORM_CONFIG_DIR` moves the whole config root.
3. Run any real upload once. The command prints an authorization URL to
   stderr and opens it in the browser. Approve the two scopes:
   - `https://www.googleapis.com/auth/youtube.upload` (videos.insert)
   - `https://www.googleapis.com/auth/youtube.force-ssl` (thumbnails.set and
     later metadata edits; the same write scope `youtube-analytics` holds)

   Google redirects to a loopback listener on `127.0.0.1` with a random port,
   so nothing needs to be configured on the client beyond "Desktop app". The
   refresh token lands in `~/.config/longform-pipeline/youtube/token-upload.json`
   with mode 0600. It is a credential: never print it, never commit it.
   Delete the file to force re-authorization. The browser flow times out
   after five minutes.

## Usage

```
longform yt-upload --file <mp4> --title <t> [--description <d>]
    [--chapters-file <json>] [--thumbnail <jpg|png>]
    [--privacy public|unlisted|private] [--publish-at RFC3339]
    [--tags a,b] [--category 22] [--made-for-kids=false] [--dry-run]
```

| Flag | Notes |
|---|---|
| `--file` | Required. Any container YouTube accepts; the pipeline hands it an mp4. |
| `--title` | Required, at most 100 characters, no `<` or `>`. |
| `--description` | At most 5000 bytes including the appended chapter block. No `<` or `>`. |
| `--chapters-file` | JSON array `[{"at_ms":0,"title":"Intro"}, ...]`. Rendered as `m:ss Title` lines (`h:mm:ss` past an hour) and appended after a blank line. |
| `--thumbnail` | `.jpg`, `.jpeg` or `.png`, at most 2 MB. Sent with `thumbnails.set` after the insert. Custom thumbnails need a phone-verified channel. |
| `--privacy` | Default `private`. `public` must be asked for explicitly. |
| `--publish-at` | RFC3339 in any zone (`2026-10-22T09:00:00-07:00`); sent as UTC `status.publishAt`. Must be in the future and implies `private` (YouTube flips it to public at that time). Passing `--privacy public` with it is an error. |
| `--tags` | Comma-separated, 500 characters total at most. |
| `--category` | Numeric category id. 22 People & Blogs, 28 Science & Technology, 27 Education. Omitted when unset. |
| `--made-for-kids` | Default false. The false value is always sent; YouTube requires the declaration. |
| `--dry-run` | Validate flags and files, print the `videos.insert` body to stdout, touch no network. |

### Chapters

YouTube reads chapters from timestamp lines in the description and only
renders them when the first line is `0:00`, there are at least three, they
ascend, and each chapter lasts at least ten seconds. A file that breaks any of
those rules is rejected before upload (exit 65), because YouTube itself would
accept the upload and silently show no chapters.

```json
[
  {"at_ms": 0,      "title": "Intro"},
  {"at_ms": 65000,  "title": "The problem"},
  {"at_ms": 312000, "title": "What I tried"},
  {"at_ms": 900000, "title": "Wrap up"}
]
```

becomes, under the description:

```
0:00 Intro
1:05 The problem
5:12 What I tried
15:00 Wrap up
```

### Output

On success stdout carries one JSON object:

```json
{
  "video_id": "dQw4w9WgXcQ",
  "url": "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
  "privacy": "private",
  "publish_at": "2026-10-22T16:00:00Z"
}
```

`publish_at` is `""` when not scheduled. `thumbnail_set: true` appears when a
thumbnail was sent. `locked_private: true` appears when YouTube reported a
different privacy than requested (see below). Upload progress goes to stderr
as `yt-upload: 42.0% (11.3 MB / 27.0 MB)`.

If the video is created but the thumbnail call fails, the JSON is still
printed (so the id is not lost) and the exit code is 75; set the thumbnail in
YouTube Studio or fix the cause.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Uploaded (or dry run printed). |
| 64 | Flag problem: missing title, bad privacy, unparsable `--publish-at`, conflicting flags. |
| 65 | File problem: video or thumbnail missing or empty, chapters file unreadable or breaking YouTube's rules. |
| 70 | Auth or API error that needs a human (bad client secret, consent refused, 400/401/403 other than quota). |
| 75 | Temporary: network, 5xx, 429, quota exhausted, or thumbnail failed after a successful insert. Retry later. |

### Quota

`videos.insert` costs 1600 units and `thumbnails.set` 50 of the default
10,000 units per day, so about six uploads a day per project. Quota resets
at midnight Pacific. A `quotaExceeded` response exits 75.

## Unverified OAuth apps: videos locked private

YouTube Data API clients created after 28 July 2020 that have not passed
Google's **API compliance audit** have their uploads set to **private** no
matter what `status.privacyStatus` says, and the privacy cannot be changed
until the audit completes. A scheduled `publishAt` will also not fire. The
upload itself succeeds, so this is easy to miss.

How to check:

1. `yt-upload` compares the privacy it asked for with the one in the insert
   response. On a mismatch it prints a warning on stderr and sets
   `locked_private: true` in the JSON. Treat that as the signal.
2. In YouTube Studio -> Content, the video shows **Private** with a lock
   icon and a notice that it was locked because it was uploaded by an
   unverified app. The Visibility control is disabled.
3. Via the API: `videos.list?part=status&id=<id>` returns
   `status.privacyStatus: "private"` with no `publishAt`, even though you
   sent one.

What to do:

- Request the audit from the project's Cloud Console (YouTube Data API ->
  Quotas and compliance -> "Request audit") or through Google's YouTube API
  Services compliance form. Explain the use: uploading your own videos to
  your own channel. Audits take days to weeks.
- Until then the video has to be set public by hand in YouTube Studio after
  the audit, or re-uploaded through the native OpusClip post (the D8
  default), which is not affected.
- Uploads made before the audit stay locked after it; Google does not
  unlock them retroactively. Request an unlock through the audit follow-up
  if a specific video matters.

The same OAuth client `youtube-analytics` uses may already be past this if
it was created before July 2020 or has been audited; check one upload with
`--privacy unlisted` and read `locked_private` before relying on scheduling.

## Files

| Path | Purpose |
|---|---|
| `internal/youtube/chapters.go` | Chapter parsing, validation, `m:ss` formatting, description append. |
| `internal/youtube/metadata.go` | `Request`, validation against YouTube's limits, `videos.insert` body. |
| `internal/youtube/oauth.go` | Installed-app OAuth with loopback redirect and the local token file. |
| `internal/youtube/upload.go` | Resumable upload with progress, `thumbnails.set`, file checks. |
| `internal/youtube/options.go` | Flag parsing for the command. |
| `internal/cli/cmd_ytupload.go` | The `yt-upload` subcommand: exit codes, progress line, JSON output. |
