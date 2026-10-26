package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/michaellady/longform-pipeline/internal/media"
	"github.com/michaellady/longform-pipeline/internal/media/prepare"
	"github.com/michaellady/longform-pipeline/internal/model"
)

func init() {
	Register(Command{Name: "prepare", Short: "Remux/tonemap, stabilise, transcribe, censor a staged job into delivery.mp4", Run: runPrepare})
}

func runPrepare(args []string) int {
	fs := flag.NewFlagSet("prepare", flag.ContinueOnError)
	censorList := fs.String("censor-list", "", "override the censor word list (default: LONGFORM_CENSOR_LIST, config dir, plugin dir)")
	whisper := fs.String("whisper-cli", "", "path to whisper-cli (default: PATH)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: longform prepare [flags] <jobdir|jobid>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 64
	}
	job, err := model.LoadJob(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "prepare:", err)
		return 65
	}
	if job.AtLeast(model.StatePrepared) {
		fmt.Printf("%s already %s\n", job.ID, job.State)
		return 0
	}
	if job.State != model.StateStaged {
		fmt.Fprintf(os.Stderr, "prepare: %s is %s, need staged\n", job.ID, job.State)
		return 65
	}
	prof, err := model.LoadProfile(job.Profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "prepare:", err)
		return 65
	}
	lock, err := model.Acquire(job.Dir(), "prepare")
	if err != nil {
		fmt.Fprintln(os.Stderr, "prepare:", err)
		if errors.Is(err, model.ErrLocked) {
			return 75
		}
		return 70
	}
	defer lock.Release()
	tools, err := media.FindTools()
	if err != nil {
		fmt.Fprintln(os.Stderr, "prepare:", err)
		return 69
	}
	err = prepare.Run(job, prof, prepare.Options{Tools: tools, WhisperCLI: *whisper, CensorList: *censorList, Log: os.Stdout})
	if err != nil {
		fmt.Fprintln(os.Stderr, "prepare:", err)
		// 70: ffmpeg failed, the source is HDR and this ffmpeg cannot
		// convert it correctly, or an encode came out at an irregular frame
		// rate (a tool problem on this Mac, not a bad job).
		if errors.Is(err, media.ErrToolFailed) || errors.Is(err, media.ErrNoHDRConversion) || errors.Is(err, media.ErrIrregularTiming) {
			return 70
		}
		return 65
	}
	fmt.Printf("%s %s -> %s\n", job.ID, job.State, job.Path(prepare.DeliveryOut))
	return 0
}
