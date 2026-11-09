package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/michaellady/longform-pipeline/internal/brief"
	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/newsletter"
	"github.com/michaellady/longform-pipeline/internal/statics"
)

func init() {
	Register(Command{Name: "brief", Short: "render | parse-decision | render-statics | render-carousel | render-html | newsletter-cta for a job's review/draft.json", Run: runBrief})
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
	case "render-carousel":
		return runBriefRenderCarousel(args[1:])
	case "render-html":
		return runBriefRenderHTML(args[1:])
	case "newsletter-cta":
		return runBriefNewsletterCTA(args[1:])
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
  longform brief render-carousel --job <jobdir> [--only <static-id>] [--illustrate] [--gen-interval 15s]
                                 newsletter-carousel engine: check slides, generate missing illustrations
                                 (--illustrate), render statics/<id>/slide-NN.png; one JSON line per static
  longform brief render-html     --job <jobdir> [--only <static-id>]
                                 custom-html engine: render each static's inputs.pages (skill-designed HTML)
                                 to statics/<id>/slide-NN.png at 1080x1350; one JSON line per static
  longform brief newsletter-cta  (--profile <name> | --job <jobdir>) [--json]
                                 print the canonical CTA: _shared/cta.sh "<newest newsletter title>"

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
	if prof.NewsletterCarousel() || prof.CustomHTML() {
		fmt.Fprintf(os.Stderr, "brief render-statics: profile uses statics.engine %s; its statics are rendered in the draft phase (brief render-carousel / render-html), nothing to do\n", prof.Statics.Engine)
		return 0
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

func runBriefRenderCarousel(args []string) int {
	fs := flag.NewFlagSet("brief render-carousel", flag.ContinueOnError)
	jobDir := fs.String("job", "", "job directory or id")
	only := fs.String("only", "", "render only this static id")
	illustrate := fs.Bool("illustrate", false, "generate missing illustrations with carousel_dir/templates/gen_illustration.py (paid Gemini calls)")
	interval := fs.Duration("gen-interval", 15*time.Second, "pause between illustration calls (Gemini rate limit)")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	job, prof, d, code := loadJobAndDraft(*jobDir)
	if code != 0 {
		return code
	}
	if !prof.NewsletterCarousel() {
		fmt.Fprintln(os.Stderr, "brief render-carousel: the profile does not set statics.engine newsletter-carousel; use render-statics")
		return 65
	}
	words, err := statics.LoadTranscriptWords(job.Dir())
	if err != nil || len(words) == 0 {
		fmt.Fprintf(os.Stderr, "brief render-carousel: quote slides are checked against analysis/transcript.json, which is missing or empty (%v)\n", err)
		return 65
	}
	results, err := statics.RenderCarousels(job.Dir(), d, statics.CarouselOptions{
		Dir: prof.Statics.CarouselDir, Only: *only, Illustrate: *illustrate, GenInterval: *interval,
		Transcript: words, Log: os.Stderr,
	})
	enc := json.NewEncoder(os.Stdout)
	for _, r := range results {
		_ = enc.Encode(r)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "brief render-carousel:", err)
		if errors.Is(err, statics.ErrCarouselInput) {
			return 65
		}
		return 70
	}
	if len(results) > 0 {
		job.Artifacts.Statics = "statics"
		if err := job.Save(); err != nil {
			fmt.Fprintln(os.Stderr, "brief render-carousel:", err)
			return 70
		}
	}
	return 0
}

func runBriefNewsletterCTA(args []string) int {
	fs := flag.NewFlagSet("brief newsletter-cta", flag.ContinueOnError)
	profName := fs.String("profile", "", "profile name")
	jobDir := fs.String("job", "", "job directory or id (its profile is used)")
	asJSON := fs.Bool("json", false, "print {title, url, published_at, cta}")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	name := *profName
	if name == "" && *jobDir != "" {
		job, err := model.LoadJob(*jobDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "brief newsletter-cta:", err)
			return 65
		}
		name = job.Profile
	}
	if name == "" {
		fmt.Fprintln(os.Stderr, "brief newsletter-cta: --profile or --job is required")
		return 64
	}
	prof, err := model.LoadProfile(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "brief newsletter-cta:", err)
		return 65
	}
	if prof.Copy.VoiceCorpus == "" {
		fmt.Fprintln(os.Stderr, "brief newsletter-cta: profile has no copy.voice_corpus")
		return 65
	}
	cta, post, err := newsletter.CanonicalCTA(prof.Copy.VoiceCorpus, prof.CTAScriptPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "brief newsletter-cta:", err)
		return 65
	}
	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"title": post.Title, "url": post.URL, "published_at": post.PublishedAt, "cta": cta})
		return 0
	}
	fmt.Println(cta)
	return 0
}

func runBriefRenderHTML(args []string) int {
	fs := flag.NewFlagSet("brief render-html", flag.ContinueOnError)
	jobDir := fs.String("job", "", "job directory or id")
	only := fs.String("only", "", "render only this static id")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	job, prof, d, code := loadJobAndDraft(*jobDir)
	if code != 0 {
		return code
	}
	if !prof.CustomHTML() {
		fmt.Fprintln(os.Stderr, "brief render-html: the profile does not set statics.engine custom-html")
		return 65
	}
	results, err := statics.RenderHTMLPages(job.Dir(), d, statics.HTMLOptions{Only: *only})
	enc := json.NewEncoder(os.Stdout)
	for _, r := range results {
		_ = enc.Encode(r)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "brief render-html:", err)
		if errors.Is(err, statics.ErrCarouselInput) {
			return 65
		}
		return 70
	}
	if len(results) > 0 {
		job.Artifacts.Statics = "statics"
		if err := job.Save(); err != nil {
			fmt.Fprintln(os.Stderr, "brief render-html:", err)
			return 70
		}
	}
	return 0
}
