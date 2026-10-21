package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/michaellady/longform-pipeline/internal/youtube"
)

func init() {
	Register(Command{
		Name:  "yt-upload",
		Short: "Upload a finished video to YouTube (metadata, chapters, thumbnail, schedule)",
		Run:   runYTUpload,
	})
}

func runYTUpload(args []string) int {
	req, opts, err := youtube.ParseArgs(args, os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, "yt-upload:", err)
		var ue *youtube.UsageError
		if errors.As(err, &ue) {
			return 64
		}
		return 65
	}

	body, err := youtube.BuildVideo(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "yt-upload:", err)
		return 64
	}
	if err := youtube.CheckFiles(req); err != nil {
		fmt.Fprintln(os.Stderr, "yt-upload:", err)
		return 65
	}

	if opts.DryRun {
		st, _ := os.Stat(req.File)
		fmt.Fprintf(os.Stderr, "yt-upload: dry run; would POST videos.insert part=%v with the body below and upload %s (%d bytes)", youtube.VideoParts, req.File, st.Size())
		if req.Thumbnail != "" {
			fmt.Fprintf(os.Stderr, ", then thumbnails.set with %s", req.Thumbnail)
		}
		fmt.Fprintln(os.Stderr, "; no network")
		return printJSON(body)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	svc, err := youtube.NewService(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "yt-upload:", err)
		return 70
	}

	progressed := false
	progress := func(sent, total int64) {
		progressed = true
		if total > 0 {
			fmt.Fprintf(os.Stderr, "\ryt-upload: %5.1f%% (%s / %s)", 100*float64(sent)/float64(total), humanBytes(sent), humanBytes(total))
		} else {
			fmt.Fprintf(os.Stderr, "\ryt-upload: %s sent", humanBytes(sent))
		}
	}
	res, err := youtube.Upload(ctx, svc, req, progress)
	if progressed {
		fmt.Fprintln(os.Stderr)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "yt-upload:", err)
		if res != nil {
			// The video was created; only the thumbnail failed. Print the
			// result so the id is not lost, and signal retry.
			printJSON(res)
			return 75
		}
		if youtube.IsTemporary(err) {
			return 75
		}
		return 70
	}
	if res.LockedPrivate {
		fmt.Fprintf(os.Stderr, "yt-upload: WARNING: requested privacy %q but YouTube reports %q. This is the unverified-OAuth-app lock; see docs/youtube.md.\n", body.Status.PrivacyStatus, res.Privacy)
	}
	return printJSON(res)
}

// printJSON is shared with cmd_opus.go (same package).

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
