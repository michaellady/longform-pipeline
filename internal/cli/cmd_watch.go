package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/michaellady/longform-pipeline/internal/ingest"
	"github.com/michaellady/longform-pipeline/internal/model"
)

func init() {
	Register(Command{Name: "watch", Short: "Stage inbox files into jobs and drive them through prepare, upload, wait; monitor, expire and close after approval", Run: runWatch})
}

func runWatch(args []string) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	profile := fs.String("profile", "all", "profile name, or all")
	once := fs.Bool("once", false, "run one pass and exit (launchd WatchPaths mode)")
	stable := fs.Int("stable-seconds", 60, "seconds a file's size and mtime must hold before it is staged")
	interval := fs.Duration("interval", 10*time.Minute, "pause between passes without --once")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "watch: takes no positional arguments")
		return 64
	}
	lock, err := model.Acquire(model.DataRoot(), "watch")
	if errors.Is(err, model.ErrLocked) {
		fmt.Fprintln(os.Stderr, "watch:", err)
		return 75
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "watch: lock:", err)
		return 70
	}
	defer lock.Release()

	self, err := ingest.SelfExecutable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "watch: executable:", err)
		return 70
	}
	logf := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, time.Now().Format("2006-01-02 15:04:05 ")+format+"\n", a...)
	}
	d := &ingest.Driver{Self: self, Logf: logf}
	opts := ingest.Options{StableSeconds: *stable, Once: *once, Logf: logf}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		if err := d.Pass(*profile, opts); err != nil {
			logf("watch: %v", err)
			if *once {
				return 70
			}
		}
		if *once {
			return 0
		}
		select {
		case <-ctx.Done():
			logf("watch: stopping")
			return 0
		case <-time.After(*interval):
		}
	}
}
