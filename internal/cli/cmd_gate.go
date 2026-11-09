package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/michaellady/longform-pipeline/internal/gates"
	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/newsletter"
	"github.com/michaellady/longform-pipeline/internal/statics"
)

func init() {
	Register(Command{
		Name:  "gate",
		Short: "Deterministic pre-publish gates: gate validate --profile <p> --cells <file> [...] | gate statics --job <dir> [--hosted] [--list]",
		Run: func(args []string) int {
			if len(args) > 0 && args[0] == "statics" {
				return runGateStatics(args[1:])
			}
			return gates.Main(args, os.Stdin, os.Stdout, os.Stderr)
		},
	})
}

// runGateStatics is the image-caption alignment gate for static image
// posts: every static about to post needs a PASS visual review bound to its
// exact caption and current image bytes (and, with --hosted, to identical
// hosted bytes), Instagram images inside 0.8..1.91, and with
// cta.static_cta newsletter-canon the canonical CTA as the caption ending.
func runGateStatics(args []string) int {
	fs := flag.NewFlagSet("gate statics", flag.ContinueOnError)
	jobDir := fs.String("job", "", "job directory or id")
	hosted := fs.Bool("hosted", false, "also fetch every visual_review.images[].url and compare its sha256 with the reviewed bytes")
	list := fs.Bool("list", false, "print each static's rendered PNGs with sha256 and size (for visual_review.images), no checks")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	job, prof, d, code := loadJobAndDraft(*jobDir)
	if code != 0 {
		return code
	}
	if *list {
		_ = json.NewEncoder(os.Stdout).Encode(statics.ListImages(job.Dir(), d))
		return 0
	}
	opt := statics.GateOptions{Hosted: *hosted, Schedule: job.AtLeast(model.StateApproved)}
	if prof != nil && prof.CTA.StaticCTA == model.StaticCTANewsletterCanon {
		cta, post, err := newsletter.CanonicalCTA(prof.Copy.VoiceCorpus, prof.CTAScriptPath())
		if err != nil {
			fmt.Fprintln(os.Stderr, "gate statics: canonical CTA:", err)
			return 65
		}
		fmt.Fprintf(os.Stderr, "gate statics: canonical CTA from newsletter %q\n", post.Title)
		opt.CTA = cta
	}
	verdicts := statics.Gate(job.Dir(), d, opt)
	if len(verdicts) == 0 {
		fmt.Fprintln(os.Stderr, "gate statics: every static passes")
		return 0
	}
	enc := json.NewEncoder(os.Stdout)
	for _, v := range verdicts {
		_ = enc.Encode(v)
	}
	fmt.Fprintf(os.Stderr, "gate statics: %d verdicts\n", len(verdicts))
	return 65
}
