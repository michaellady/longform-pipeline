package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/michaellady/longform-pipeline/internal/brief"
	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/statics"
)

func init() {
	Register(Command{Name: "brief", Short: "render | parse-decision | render-statics for a job's review/draft.json", Run: runBrief})
}

func runBrief(args []string) int {
	if len(args) == 0 {
		briefUsage()
		return 64
	}
	switch args[0] {
	case "render":
		return runBriefRender(args[1:])
	case "parse-decision":
		return runBriefParseDecision(args[1:])
	case "render-statics":
		return runBriefRenderStatics(args[1:])
	case "-h", "--help", "help":
		briefUsage()
		return 0
	}
	fmt.Fprintf(os.Stderr, "longform brief: unknown subcommand %q\n\n", args[0])
	briefUsage()
	return 64
}

func briefUsage() {
	fmt.Fprintln(os.Stderr, `Usage:
  longform brief render         --job <jobdir>            write review/brief.md from review/draft.json (clipped -> drafted)
  longform brief parse-decision --job <jobdir> "<grammar>" write review/decision.json + review/schedule.json (drafted -> approved)
  longform brief render-statics --job <jobdir> [--only <format>]  render statics/<id>/ PNGs and PDFs from the profile templates

Grammar: approve | approve except 3,7 | hold <platform> | drop <n> | swap thumbnail <n> | decide <id> <option> | reject [note]; join with ';'.`)
}

// loadJobAndDraft is the shared preamble; the profile is optional for the
// two review commands (defaults apply with a warning) and required by the
// caller when brand fields are rendered.
func loadJobAndDraft(jobDir string) (*model.Job, *model.Profile, *brief.Draft, int) {
	if jobDir == "" {
		fmt.Fprintln(os.Stderr, "brief: --job is required")
		return nil, nil, nil, 64
	}
	job, err := model.LoadJob(jobDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "brief:", err)
		return nil, nil, nil, 65
	}
	d, err := brief.LoadDraft(job.Dir())
	if err != nil {
		fmt.Fprintln(os.Stderr, "brief:", err)
		return nil, nil, nil, 65
	}
	prof, err := model.LoadProfile(job.Profile)
	if err != nil {
		if errors.Is(err, model.ErrNoProfile) {
			fmt.Fprintf(os.Stderr, "brief: warning: %v; using defaults\n", err)
			prof = nil
		} else {
			fmt.Fprintln(os.Stderr, "brief:", err)
			return nil, nil, nil, 65
		}
	}
	return job, prof, d, 0
}

func runBriefRender(args []string) int {
	fs := flag.NewFlagSet("brief render", flag.ContinueOnError)
	jobDir := fs.String("job", "", "job directory or id")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	job, prof, d, code := loadJobAndDraft(*jobDir)
	if code != 0 {
		return code
	}
	if _, err := brief.WriteBrief(job, prof, d, time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "brief render:", err)
		if errors.Is(err, model.ErrBadTransition) {
			return 65
		}
		return 70
	}
	fmt.Println(job.Path(brief.BriefFile))
	return 0
}

func runBriefParseDecision(args []string) int {
	fs := flag.NewFlagSet("brief parse-decision", flag.ContinueOnError)
	jobDir := fs.String("job", "", "job directory or id")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	grammar := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if grammar == "" {
		fmt.Fprintln(os.Stderr, "brief parse-decision: a grammar string is required")
		return 64
	}
	job, prof, d, code := loadJobAndDraft(*jobDir)
	if code != 0 {
		return code
	}
	rec, err := brief.ParseGrammar(grammar, d)
	if err != nil {
		fmt.Fprintln(os.Stderr, "brief parse-decision:", err)
		return 65
	}
	sch, err := brief.ApplyDecision(job, prof, d, rec, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "brief parse-decision:", err)
		if errors.Is(err, model.ErrBadTransition) {
			return 65
		}
		return 70
	}
	if rec.Approved {
		fmt.Printf("approved: %d cells -> %s\n", len(sch.Cells), job.Path(brief.ScheduleFile))
	} else {
		fmt.Printf("rejected: job back to %s\n", job.State)
	}
	return 0
}

func runBriefRenderStatics(args []string) int {
	fs := flag.NewFlagSet("brief render-statics", flag.ContinueOnError)
	jobDir := fs.String("job", "", "job directory or id")
	only := fs.String("only", "", "render only this format (quote|carousel|still|frame_carousel)")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	job, prof, d, code := loadJobAndDraft(*jobDir)
	if code != 0 {
		return code
	}
	if prof == nil {
		fmt.Fprintln(os.Stderr, "brief render-statics: a profile is required for brand placeholders")
		return 65
	}
	video := job.Artifacts.Delivery
	if video == "" {
		video = "delivery.mp4"
	}
	opts := statics.Options{
		Templates: statics.Locator{Dirs: append([]string{prof.Statics.TemplatesDir}, statics.DefaultTemplateDirs()...)},
		Video:     job.Path(video),
		Brand:     prof.Brand,
		Only:      *only,
	}
	results, err := statics.RenderAll(job.Dir(), d, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "brief render-statics:", err)
		return 70
	}
	for _, r := range results {
		fmt.Printf("%s (%s): %s\n", r.ID, r.Format, strings.Join(r.Files, " "))
	}
	if len(results) > 0 {
		job.Artifacts.Statics = "statics"
		if err := job.Save(); err != nil {
			fmt.Fprintln(os.Stderr, "brief render-statics:", err)
			return 70
		}
	}
	return 0
}
