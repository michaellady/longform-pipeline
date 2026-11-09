package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/michaellady/longform-pipeline/internal/brief"
	"github.com/michaellady/longform-pipeline/internal/media"
	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/spotify"
)

func init() {
	Register(Command{Name: "spotify", Short: "Spotify video episode: prepare (16:9 episode file + notes for the browser upload)", Run: runSpotify})
}

func spotifyHelp() int {
	fmt.Fprintln(os.Stderr, `usage: longform spotify prepare --job <jobdir|jobid> [--source <video>] [--encoder auto|videotoolbox|libx264] [--force]

  Writes <job>/spotify/episode-1080p.mp4 (16:9 H.264 High + AAC: a vertical source is
  pillarboxed over a blurred fill, a 16:9 H.264 source is remuxed), episode-notes.txt and
  episode-notes.html (from review/draft.json full_video.description: utm_source swapped,
  [opus:] tag and hashtag-only lines removed), and prints a JSON summary.

  Source, unless --source: a fresh OpusClip export of the full clip, else clips.json's
  uri_export while unexpired, else delivery.mp4 (uncaptioned; the summary says so).
  Skips work whose outputs are already up to date unless --force.
  Exit: 0 ok, 64 usage, 65 data (no draft, title/notes over Spotify limits), 69 no ffmpeg,
  70 ffmpeg failed, 75 job locked.

  Spotify has no creator upload API: the publish skill uploads the file in the browser.`)
	return 64
}

func runSpotify(args []string) int {
	if len(args) == 0 || args[0] != "prepare" {
		return spotifyHelp()
	}
	fs := flag.NewFlagSet("spotify prepare", flag.ContinueOnError)
	jobArg := fs.String("job", "", "job directory or id")
	source := fs.String("source", "", "source video (default: fresh OpusClip export, clips.json uri_export, delivery.mp4)")
	encoder := fs.String("encoder", spotify.EncoderAuto, "auto | videotoolbox | libx264")
	force := fs.Bool("force", false, "redo the download, encode and notes even when up to date")
	if err := fs.Parse(args[1:]); err != nil {
		return 64
	}
	if *jobArg == "" || fs.NArg() > 0 {
		return spotifyHelp()
	}
	switch *encoder {
	case spotify.EncoderAuto, spotify.EncoderVideoToolbox, spotify.EncoderLibx264:
	default:
		fmt.Fprintf(os.Stderr, "spotify prepare: --encoder must be auto, videotoolbox or libx264, got %q\n", *encoder)
		return 64
	}
	job, err := model.LoadJob(*jobArg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "spotify prepare:", err)
		return 65
	}
	d, err := brief.LoadDraft(job.Dir())
	if err != nil {
		fmt.Fprintln(os.Stderr, "spotify prepare: needs review/draft.json (the full video's title and description):", err)
		return 65
	}
	utm := "spotify"
	if prof, err := model.LoadProfile(job.Profile); err != nil {
		fmt.Fprintf(os.Stderr, "spotify prepare: WARN profile %s: %v; notes use utm_source=spotify\n", job.Profile, err)
	} else {
		utm = prof.SpotifyUTMSource()
		if !prof.SpotifyEnabled() {
			fmt.Fprintf(os.Stderr, "spotify prepare: WARN profile %s has no spotify block; the gate will refuse a spotify cell\n", job.Profile)
		}
	}
	tools, err := media.FindTools()
	if err != nil {
		fmt.Fprintln(os.Stderr, "spotify prepare:", err)
		return 69
	}
	lock, err := model.Acquire(job.Dir(), "spotify")
	if err != nil {
		fmt.Fprintln(os.Stderr, "spotify prepare:", err)
		if errors.Is(err, model.ErrLocked) {
			return 75
		}
		return 70
	}
	defer lock.Release()

	ctx, cancel := signalContext()
	defer cancel()
	projectID, clipID := d.FullVideo.ProjectID, d.FullVideo.ClipID
	if projectID == "" {
		projectID = job.OpusClip.FullProjectID
	}
	if clipID == "" {
		clipID = job.OpusClip.FullClipID
	}
	tools.Log = os.Stderr
	opts := spotify.Options{
		JobDir: job.Dir(), Source: *source, Title: d.FullVideo.Title, Description: d.FullVideo.Description,
		UTMSource: utm, Encoder: *encoder, Force: *force, Tools: tools,
		ClipsExportURL: clipsExportURL(job, clipID),
		Download: func(ctx context.Context, u, dst string) error {
			_, err := download(ctx, u, dst)
			return err
		},
	}
	if projectID != "" && clipID != "" && *source == "" {
		opts.FreshExportURL = func(ctx context.Context) (string, error) {
			client, err := newOpusClient()
			if err != nil {
				return "", err
			}
			cl, err := client.DescribeClip(ctx, projectID, clipID, "")
			if err != nil {
				return "", err
			}
			if cl.RenderPending() {
				return "", errors.New("the full clip is still rendering")
			}
			return cl.URIForExport, nil
		}
	}
	res, err := spotify.Prepare(ctx, opts)
	if res != nil && res.Source != "" {
		printJSON(res)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "spotify prepare:", err)
		switch {
		case errors.Is(err, spotify.ErrLimits):
			return 65
		case errors.Is(err, media.ErrToolFailed):
			return 70
		}
		return 65
	}
	return 0
}

// clipsExportURL is clips.json's uri_export for the full clip, "" when the
// file or the clip is absent.
func clipsExportURL(job *model.Job, clipID string) string {
	raw, err := os.ReadFile(job.Path("clips.json"))
	if err != nil {
		return ""
	}
	var cf ClipsFile
	if json.Unmarshal(raw, &cf) != nil {
		return ""
	}
	if clipID == "" {
		clipID = cf.FullClipID
	}
	for _, c := range cf.Clips {
		if c.ClipID == clipID || (clipID == "" && c.IsFull) {
			return c.URIExport
		}
	}
	return ""
}
