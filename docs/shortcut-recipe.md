# iOS Shortcuts: get a recording from the phone to the Mac

Each profile has two Share-sheet shortcuts. Both appear when you share a video from
Photos; the Mac's launchd watcher does everything after that.

| Shortcut | What the phone does | Use it for |
|---|---|---|
| **Mark for <Brand>** | Adds the video to the profile's Photos album (`photos.album`). Nothing is copied: iCloud Photos uploads the original as it always does, and the Mac's Photos feed picks it up once it has synced. | Everything recorded with the Camera app or saved to Photos. This is the default when the profile has `photos.enabled`. |
| **Send to <profile> inbox** | Copies the file into `iCloud Drive/Shortcuts/longform-inbox/<profile>/` with Save File. A 20-minute 4K video is copied byte for byte through Shortcuts first. | Files that are not in Photos (the Files app, downloads, other apps' exports), or a profile without the Photos feed. |

Replace `<profile>` below with the profile name (`evc`, `sandbox-bjj`, ...), `<Brand>`
with its `brand.display`, and `<album>` with its `photos.album` (for example
`longform-evc`).

## Mark for <Brand> (Photos album)

### Once: create the album on the phone

The shortcut adds to an album that already exists; it does not create one.

1. Open **Photos** on the iPhone → **Albums** → **+** → **New Album**.
2. Name it exactly as the profile's `photos.album` (same spelling, case and spaces),
   for example `longform-evc`.

With iCloud Photos on, the album syncs to the Mac's Photos library within a minute or
so, which is where the watcher's Photos feed reads it. Only movies in the album are
fed; photos added to it are ignored.

### Steps (hand-built on the iPhone, about one minute)

1. Open **Shortcuts**, tap **+** to create a new Shortcut.
2. Rename it **Mark for <Brand>**.
3. Tap the **(i)** info button, then:
   - turn **Show in Share Sheet** ON
   - under **Share Sheet Types** pick **Images** and **Media** only
4. The **Receive input from Share Sheet** action appears at the top. Set its types to
   **Images** and **Media**. Under "If there's no input" choose **Stop and Respond**.
5. Add action **Remove from Photo Album** (search "Remove from"). Set the input to
   **Shortcut Input** and **Album** to `<album>`. This step only makes a second mark of
   the same video harmless (see below); it does nothing to a video not yet in the album.
6. Add action **Save to Photos** (older iOS: **Save to Photo Album**; search "Save to
   Photo"). Set the input to **Shortcut Input** and **Album** to `<album>`.
7. Add action **Show Notification**. Title `Marked for <Brand>`, body
   `Added to the <album> album. The Mac picks it up once iCloud Photos has synced it.`
8. Tap **Done**.

Shortcuts has no usable "Add to Album" action: Photos' own *Add Photos To Album* intent
is hidden from the action library, and a shortcut file containing it is refused at
import. **Save to Photos** with an album is what adds an item to an album. Given a
video shared from Photos that is not in the album yet, it adds that same video to the
album and copies nothing; given one that is already in the album, it saves a second
copy. Removing it from the album first (step 5) means a repeated mark puts the original
back instead of duplicating a multi-gigabyte recording.

The first run asks for permission to access Photos. Allow it once.

### Use it

- **Single take (EVC)**: record in the Camera app, open the video in Photos, tap
  **Share**, pick **Mark for <Brand>**. It returns at once because nothing is copied.
- **Several files in one segment (BJJ)**: in Photos tap **Select**, pick every file of
  the class segment, tap **Share**, pick the Shortcut once. All of them go into the
  album; the watcher stages every synced album recording of a pass together, so
  `stitch` groups them by recording time as it does for inbox files.
- Without the shortcut: **Share** → **Add to Album** → `<album>` does the same thing.

The Mac sees a recording only after iCloud Photos has uploaded it from the phone and
downloaded the original to the Mac (Photos → Settings → iCloud → "Download Originals
to this Mac", or the profile's `photos.download_missing`). Until then the watcher
logs it as waiting and sends one notification. See `docs/profile-schema.md` →
`photos`.

### Building it on the Mac instead

```bash
longform shortcut --profile <profile> --kind mark --open
```

This builds "Mark for <Brand>" as a signed `.shortcut` under
`~/.config/longform-pipeline/shortcuts/` and opens it in the Mac's Shortcuts app.
Click **Add Shortcut** once; with Shortcuts' iCloud sync on it reaches the iPhone. The
album defaults to the profile's `photos.album`; `--album <name>` overrides it, and the
command exits `64` when neither is set.

The built shortcut is the same three actions as the hand-built one (Remove from Photo
Album, Save to Photos, Show Notification). Both actions name the album by its title,
and Shortcuts looks the album up by that name on whichever device runs it, so the Mac
does not need the phone's ID for it. If the imported shortcut shows an action it cannot
resolve, or asks for an album on every run, delete it and hand-build it with the steps
above.

Do not add Photos' *Add Photos To Album* or *Find Albums* intents to a built shortcut:
the Mac's Shortcuts refuses any shortcut file containing an Apple App Intent that is
not on its public-action list ("This shortcut can't be imported because it contains
features not supported on this device"), and *Add Photos To Album* is on none of them
(Mac, iPhone or iPad). `shortcuts sign` does not check this; only the import does.

### Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| "The photo album that was selected does not exist", or the shortcut asks which album | the album does not exist on the phone, or its name differs from `photos.album` | create it (Photos → Albums → + → New Album) with the exact name |
| "Can't Import Shortcut … contains features not supported on this device" on the Mac | the file holds an App Intent the Mac will not import (an old build used *Add Photos To Album*) | rebuild with `longform shortcut --profile <profile> --kind mark`, or hand-build on the phone |
| Shortcut missing from the Share sheet | Share Sheet Types lack Images/Media | step 3 |
| Shortcut fails on a file from the Files app | Remove from Photo Album only works on items already in Photos | use **Send to <profile> inbox** for that file |
| Two copies of a video in the album | the step-5 Remove from Photo Album is missing, and the video was marked twice | add step 5; delete the extra copy in Photos |
| Marked, but no job after a while | the original has not synced to the Mac yet, or `photos.enabled` is false | `longform status`; check Photos → Settings → iCloud on the Mac; see `docs/profile-schema.md` → `photos` |

## Send to <profile> inbox (fallback for files not in Photos)

It saves the selected file(s) into the profile's iCloud Drive inbox. Build it once on
the iPhone (about two minutes).

The folder `iCloud Drive/Shortcuts/longform-inbox/<profile>` is created by
`/longform-pipeline:longform-setup` stage 2; create it from Files if you build the
Shortcut first.

### Steps

1. Open **Shortcuts** on the iPhone, tap **+** to create a new Shortcut.
2. Tap the name at the top and rename it **Send to <profile> inbox**. Pick an icon
   if you like.
3. Tap the **(i)** info button at the bottom (or the name chevron on older iOS), then:
   - turn **Show in Share Sheet** ON
   - under **Share Sheet Types** pick **Media** only (deselect everything else)
4. Add action **Receive input from Share Sheet** (search "Receive"). It is added
   automatically when Share Sheet is enabled; set its type to **Video** (and
   **Images** off). Under "If there's no input" choose **Continue**.
5. Add action **Save File** (search "Save File"). Configure it:
   - input: **Shortcut Input**
   - **Service**: iCloud Drive
   - **Ask Where to Save**: OFF (tap the action's expand arrow to see it)
   - **Destination Path**: `longform-inbox/<profile>/` (the trailing slash makes it a folder; without it the last segment becomes the file name)
     (type it; no leading slash; keep the trailing slash; Subpath stays empty)
   - **Overwrite If File Exists**: ON
6. Add action **Show Notification** with the text `Sent to <profile> inbox`.
7. Tap **Done**.

### Use it

- **Single take (EVC)**: open the video, tap **Share**, scroll the app row to
  **Send to <profile> inbox**. One tap. The notification confirms the save.
- **Several files in one segment (BJJ)**: select every file of the class segment,
  tap **Share**, pick the Shortcut once. All files land together; the watcher
  stitches files whose recording times fall inside the profile's
  `stitch.session_window_min`.

iCloud uploads the files when the phone has Wi-Fi or cellular data; the Mac
downloads them (`brctl download`) when it sees the placeholders. A 15-minute 1080p30
video is about 1.5 GB, so a Wi-Fi hop takes a few minutes each way.

### Verify

On the Mac, after a minute or two:

```bash
ls ~/Library/Mobile\ Documents/iCloud~is~workflow~my~workflows/Documents/longform-inbox/<profile>/
longform status
tail -f ~/Library/Logs/longform-pipeline/watch.log
```

The job appears as `<YYYY-MM-DD>-<profile>-<hash8>` in state `staged`, then
`prepared`, then `uploaded`. The whole thing is hands-off until the brief arrives.

### AirDrop alternative

AirDrop the file to the Mac and move it into the same folder. The watcher does not
care how the file got there; dedupe is by content hash, so sending the same video
both ways is harmless.

### Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Shortcut asks where to save every time | "Ask Where to Save" is ON | step 5 |
| File saved to iCloud root | Destination Path empty or has a leading slash | type `longform-inbox/<profile>/` with the trailing slash |
| Shortcut missing from the Share sheet | Share Sheet Types does not include Media | step 3 |
| File lands but no job | watcher not loaded, or still an iCloud placeholder | `make launchd-install`; wait for the 10-minute tick |
| Job created twice | different file content (re-export changes bytes) | expected; the second is a new job |

### Building it on the Mac instead

The wizard (stage 9) and `longform shortcut --profile <profile> --open` build the
same shortcut as a signed `.shortcut` file under
`~/.config/longform-pipeline/shortcuts/` and open it in the Mac's Shortcuts app.
Click **Add Shortcut** once; with Shortcuts' iCloud sync on, it appears on the
iPhone within a minute, in the Share sheet under "Send to <brand> inbox".
Signing uses the macOS `shortcuts sign` tool (macOS 12 or later), so the file
imports on any of your devices. The steps above remain the fallback.
