# iOS Shortcut: send a recording to the inbox

One Shortcut per profile. It appears in the Share sheet of Photos and the Camera roll,
saves the selected video(s) into the profile's iCloud Drive inbox, and the Mac's
launchd watcher does the rest. Build it once on the iPhone (about two minutes).

Replace `<profile>` below with the profile name (`evc`, `sandbox-bjj`, ...). The
folder `iCloud Drive/Shortcuts/longform-inbox/<profile>` is created by `/longform-pipeline:longform-setup`
stage 2; create it from Files if you build the Shortcut first.

## Steps

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

## Use it

- **Single take (EVC)**: record in the Camera app (standard Video mode, vertical,
  1080p30 or 4K30), open the video in Photos, tap **Share**, scroll the app row to
  **Send to <profile> inbox**. One tap. The notification confirms the save.
- **Several files in one segment (BJJ)**: in Photos tap **Select**, pick every file of
  the class segment, tap **Share**, pick the Shortcut once. All files land together; the
  watcher stitches files whose recording times fall inside the profile's
  `stitch.session_window_min`.

iCloud uploads the files when the phone has Wi-Fi or cellular data; the Mac
downloads them (`brctl download`) when it sees the placeholders. A 15-minute 1080p30
video is about 1.5 GB, so a Wi-Fi hop takes a few minutes each way.

## Verify

On the Mac, after a minute or two:

```bash
ls ~/Library/Mobile\ Documents/iCloud~is~workflow~my~workflows/Documents/longform-inbox/<profile>/
longform status
tail -f ~/Library/Logs/longform-pipeline/watch.log
```

The job appears as `<YYYY-MM-DD>-<profile>-<hash8>` in state `staged`, then
`prepared`, then `uploaded`. The whole thing is hands-off until the brief arrives.

## AirDrop alternative

AirDrop the file to the Mac and move it into the same folder. The watcher does not
care how the file got there; dedupe is by content hash, so sending the same video
both ways is harmless.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Shortcut asks where to save every time | "Ask Where to Save" is ON | step 5 |
| File saved to iCloud root | Destination Path empty or has a leading slash | type `longform-inbox/<profile>/` with the trailing slash |
| Shortcut missing from the Share sheet | Share Sheet Types does not include Media | step 3 |
| File lands but no job | watcher not loaded, or still an iCloud placeholder | `make launchd-install`; wait for the 10-minute tick |
| Job created twice | different file content (re-export changes bytes) | expected; the second is a new job |

## Building it on the Mac instead

The wizard (stage 9) and `longform shortcut --profile <profile> --open` build the
same shortcut as a signed `.shortcut` file under
`~/.config/longform-pipeline/shortcuts/` and open it in the Mac's Shortcuts app.
Click **Add Shortcut** once; with Shortcuts' iCloud sync on, it appears on the
iPhone within a minute, in the Share sheet under "Send to <brand> inbox".
Signing uses the macOS `shortcuts sign` tool (macOS 12 or later), so the file
imports on any of your devices. The steps above remain the fallback.
