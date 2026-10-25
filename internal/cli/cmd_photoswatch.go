package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/michaellady/longform-pipeline/internal/ingest"
	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/photos"
)

func init() {
	Register(Command{Name: "photos-watch", Short: "Feed recordings tagged in a profile's Photos album into jobs: photos-watch --profile <p> [--once] [--dry-run]", Run: runPhotosWatch})
}

func runPhotosWatch(args []string) int {
	fs := flag.NewFlagSet("photos-watch", flag.ContinueOnError)
	profile := fs.String("profile", "", "profile name (required)")
	once := fs.Bool("once", false, "run one pass and exit")
	interval := fs.Duration("interval", 5*time.Minute, "pause between passes without --once")
	dryRun := fs.Bool("dry-run", false, "list the album and the feed state; stage, download and notify nothing")
	bin := fs.String("osxphotos", "", "osxphotos executable (default: profile photos.osxphotos, $"+photos.BinEnv+", PATH)")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if *profile == "" {
		fmt.Fprintln(os.Stderr, "photos-watch: --profile is required")
		return 64
	}
	p, err := model.LoadProfile(*profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "photos-watch:", err)
		return 65
	}
	if !p.Photos.Enabled || p.Photos.Album == "" {
		fmt.Fprintf(os.Stderr, "photos-watch: profile %s has no Photos album feed (set photos.enabled and photos.album)\n", p.Name)
		return 65
	}
	if *bin == "" {
		*bin = p.Photos.Osxphotos
	}
	resolved, err := photos.FindBin(*bin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "photos-watch:", err)
		return 69
	}
	logf := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, time.Now().Format("2006-01-02 15:04:05 ")+format+"\n", a...)
	}
	client := photos.Client{Bin: resolved, Library: p.Photos.Library, Logf: logf}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *dryRun {
		return photosDryRun(ctx, p, client)
	}

	// The "watch" lock is shared with `longform watch`, which also runs this
	// pass for every profile with photos.enabled. It is held only for the
	// duration of one pass, so a long-running photos-watch never starves the
	// launchd watcher; a pass that finds the lock taken is skipped, not failed.
	for {
		lock, err := model.Acquire(model.DataRoot(), "watch")
		switch {
		case errors.Is(err, model.ErrLocked):
			if *once {
				fmt.Fprintln(os.Stderr, "photos-watch:", err)
				return 75
			}
			logf("photos-watch: %v; skipping this pass", err)
		case err != nil:
			fmt.Fprintln(os.Stderr, "photos-watch: lock:", err)
			return 70
		default:
			rep, perr := ingest.PhotosPass(ctx, p, ingest.Options{Logf: logf}, photos.Options{Client: client, Logf: logf})
			lock.Release()
			switch {
			case errors.Is(perr, photos.ErrNoAccess), errors.Is(perr, photos.ErrNoBin):
				fmt.Fprintln(os.Stderr, "photos-watch:", perr)
				return 69
			case perr != nil:
				logf("photos-watch: %v", perr)
				if *once {
					return 70
				}
			default:
				logf("photos-watch %s: album %q: %d seen, %d waiting, %d deferred, %d fed now, %d done before, %d failed", p.Name, p.Photos.Album, rep.Seen, rep.Waiting, rep.Deferred, rep.Fed, rep.Done, rep.Failed)
			}
		}
		if *once {
			return 0
		}
		select {
		case <-ctx.Done():
			logf("photos-watch: stopping")
			return 0
		case <-time.After(*interval):
		}
	}
}

// photosDryRun prints the album next to the feed state without side effects;
// it is also the access check the wizard runs after launchd install.
func photosDryRun(ctx context.Context, p *model.Profile, client photos.Client) int {
	assets, err := client.List(ctx, p.Photos.Album)
	if err != nil {
		fmt.Fprintln(os.Stderr, "photos-watch:", err)
		if errors.Is(err, photos.ErrNoAccess) {
			return 69
		}
		return 70
	}
	st, err := photos.LoadState(p.Name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "photos-watch:", err)
		return 70
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "UUID\tFILE\tSIZE\tORIGINAL\tFEED\tJOB")
	n := 0
	for _, a := range assets {
		if !a.IsMovie {
			continue
		}
		n++
		orig := "waiting"
		if a.Ready() {
			orig = "on disk"
		} else if a.InCloud != nil && !*a.InCloud {
			orig = "not in iCloud yet"
		}
		feed, job := "new", "-"
		if r := st.Assets[a.UUID]; r != nil {
			feed = r.Status
			if r.JobID != "" {
				job = r.JobID
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", a.UUID, a.OriginalFilename, photos.HumanSize(a.OriginalFilesize), orig, feed, job)
	}
	w.Flush()
	fmt.Fprintf(os.Stderr, "photos-watch %s: album %q: %d movie(s); osxphotos %s\n", p.Name, p.Photos.Album, n, client.Bin)
	return 0
}
