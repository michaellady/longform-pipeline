package statics

import (
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/michaellady/longform-pipeline/internal/brief"
)

// carouselFixture builds a carousel_dir whose templates carry the same
// placeholders as carousel-newsletter's, plus fake generator and render
// commands that copy a PNG of the given size (no Gemini, no Chrome).
type carouselFixture struct {
	dir, jobDir, genLog string
	opts                CarouselOptions
}

var stubTemplates = map[string]string{
	"01-hook.html":    `<h1>{{LINE_1}} <span>{{LINE_2}}</span></h1><p>{{TEASE}}</p><img src="{{ILLUS_IMG}}">{{PAGE_CURRENT}}/{{PAGE_TOTAL}}`,
	"02-section.html": `<span>{{KICKER}}</span><h2>{{HEADLINE_1}} {{HEADLINE_ACCENT}}</h2><p>{{BODY}}</p><img src="{{ILLUS_IMG}}">{{PAGE_CURRENT}}/{{PAGE_TOTAL}}`,
	"03-quote.html":   `<blockquote>{{QUOTE}}</blockquote><div>{{ATTRIBUTION}}</div><img src="{{ILLUS_IMG}}">{{PAGE_CURRENT}}/{{PAGE_TOTAL}}`,
	"04-stat.html":    `<div>{{STAT_NUMBER}}{{STAT_UNIT}}</div><div>{{STAT_LABEL_1}}<br>{{STAT_LABEL_2}}</div><img src="{{ILLUS_IMG}}">{{PAGE_CURRENT}}/{{PAGE_TOTAL}}`,
	"05-cta.html":     `<h2>{{HEADLINE_1}}</h2>Comment {{ACCENT_WORD}}<p>{{BODY}}</p><img src="{{ILLUS_IMG}}">{{PAGE_CURRENT}}/{{PAGE_TOTAL}}`,
}

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func newCarouselFixture(t *testing.T, w, h int) *carouselFixture {
	t.Helper()
	fx := &carouselFixture{dir: t.TempDir(), jobDir: t.TempDir()}
	tdir := filepath.Join(fx.dir, "templates")
	os.MkdirAll(tdir, 0o755)
	for name, body := range stubTemplates {
		os.WriteFile(filepath.Join(tdir, name), []byte(body), 0o644)
	}
	base := filepath.Join(fx.dir, "base.png")
	writePNG(t, base, w, h)
	fx.genLog = filepath.Join(fx.dir, "gen.log")
	gen := filepath.Join(fx.dir, "gen.sh")
	os.WriteFile(gen, []byte("#!/bin/sh\nprintf '%s|%s|%s\\n' \"$1\" \"$3\" \"$4\" >> '"+fx.genLog+"'\ncp '"+base+"' \"$2\"\n"), 0o755)
	render := filepath.Join(fx.dir, "render.sh")
	os.WriteFile(render, []byte("#!/bin/sh\ngrep -q '{{' \"$1\" && exit 3\ncp '"+base+"' \"$2\"\n"), 0o755)
	os.MkdirAll(filepath.Join(fx.jobDir, "analysis"), 0o755)
	os.WriteFile(filepath.Join(fx.jobDir, "analysis", "transcript.json"), []byte(`{"words":[
	  {"w":"So,"},{"w":"the"},{"w":"agents"},{"w":"don't"},{"w":"get"},{"w":"tired."},
	  {"w":"You"},{"w":"do."},{"w":"We"},{"w":"shipped"},{"w":"14"},{"w":"repos"},{"w":"in"},{"w":"a"},{"w":"week."}]}`), 0o644)
	words, err := LoadTranscriptWords(fx.jobDir)
	if err != nil {
		t.Fatal(err)
	}
	fx.opts = CarouselOptions{Dir: fx.dir, Generator: []string{gen}, Render: []string{render}, Transcript: words}
	return fx
}

func section(n string) brief.Slide {
	return brief.Slide{Kind: "section", Scene: "robot stacking bricks " + n, Fields: map[string]string{"KICKER": "K" + n, "HEADLINE_1": "Head", "HEADLINE_ACCENT": "line", "BODY": "Body " + n}}
}

func quoteSlide(q string) brief.Slide {
	return brief.Slide{Kind: "quote", Text: q, Scene: "robot resting on a rail", Fields: map[string]string{"ATTRIBUTION": "Enterprise Vibe Code"}}
}

func stat() brief.Slide {
	return brief.Slide{Kind: "stat", Scene: "tall brick tower", Fields: map[string]string{"STAT_NUMBER": "14", "STAT_UNIT": "", "STAT_LABEL_1": "repos", "STAT_LABEL_2": "in a week"}}
}

func goodCarousel() brief.Static {
	return brief.Static{ID: "c1", Format: "carousel", Platforms: []string{"instagram", "linkedin"}, Caption: "cap", Inputs: brief.StaticInputs{Slides: []brief.Slide{
		{Kind: "hook", Scene: "robot on rails", Fields: map[string]string{"LINE_1": "Agents", "LINE_2": "don't tire", "TEASE": "You do <&> that's the point"}},
		section("2"), quoteSlide("the agents don't get tired"), stat(), quoteSlide("You do."),
		section("6"), quoteSlide("We shipped 14 repos in a week."), stat(), section("9"),
		{Kind: "cta", Scene: "robot waving", Fields: map[string]string{"HEADLINE_1": "Want the full talk?", "BODY": "I'll DM it."}},
	}}}
}

func TestVerbatimIn(t *testing.T) {
	words := CleanWords("So, the agents don't get tired. You do.")
	for q, want := range map[string]bool{
		"the agents don't get tired": true,
		"THE AGENTS DONT GET TIRED!": true, // case and punctuation are whisper's guess
		"the agents never get tired": false,
		"agents get tired":           false, // adjacency matters
		"tired. You do.":             true,
		"":                           false,
	} {
		if got := VerbatimIn(q, words); got != want {
			t.Errorf("VerbatimIn(%q) = %v, want %v", q, got, want)
		}
	}
}

func TestRenderCarouselsGeneratesFillsAndRenders(t *testing.T) {
	fx := newCarouselFixture(t, Width, Height)
	quote := brief.Static{ID: "q1", Format: "quote", Platforms: []string{"facebook"}, Caption: "q", Inputs: brief.StaticInputs{Slides: []brief.Slide{quoteSlide("We shipped 14 repos")}}}
	d := &brief.Draft{Statics: []brief.Static{goodCarousel(), quote, {ID: "s9", Format: "still"}}}
	fx.opts.Illustrate = true
	res, err := RenderCarousels(fx.jobDir, d, fx.opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || len(res[0].Files) != 10 || len(res[1].Files) != 1 {
		t.Fatalf("results = %+v", res)
	}
	if f := res[0].Files[9]; f.Path != "statics/c1/slide-10.png" || f.Width != Width || len(f.SHA256) != 64 {
		t.Errorf("file = %+v", f)
	}
	log, _ := os.ReadFile(fx.genLog)
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(lines) != 11 || lines[0] != "robot on rails|--aspect|4:3" || !strings.HasSuffix(lines[2], "|--aspect|16:9") || !strings.HasSuffix(lines[1], "9:16") {
		t.Fatalf("generator calls:\n%s", log)
	}
	html, _ := os.ReadFile(filepath.Join(fx.jobDir, "statics", "c1", "html", "slide-01.html"))
	if !strings.Contains(string(html), "You do &lt;&amp;&gt; that&#39;s the point") || !strings.Contains(string(html), "1/10") || !strings.Contains(string(html), "file://") {
		t.Errorf("slide 1 html:\n%s", html)
	}
	cta, _ := os.ReadFile(filepath.Join(fx.jobDir, "statics", "c1", "html", "slide-10.html"))
	if !strings.Contains(string(cta), "Comment newsletter") {
		t.Errorf("CTA accent word defaults to newsletter:\n%s", cta)
	}
	var ix Result
	b, _ := os.ReadFile(filepath.Join(fx.jobDir, "statics", "c1", "index.json"))
	if json.Unmarshal(b, &ix); len(ix.Files) != 10 || len(ix.Platforms["linkedin"]) != 10 {
		t.Errorf("index.json = %s", b)
	}

	// A second run reuses the illustrations (no new generator calls) and
	// drops slides a shorter render no longer has.
	os.WriteFile(filepath.Join(fx.jobDir, "statics", "q1", "slide-02.png"), []byte("stale"), 0o644)
	if _, err := RenderCarousels(fx.jobDir, d, fx.opts); err != nil {
		t.Fatal(err)
	}
	log2, _ := os.ReadFile(fx.genLog)
	if string(log2) != string(log) {
		t.Errorf("illustrations were regenerated:\n%s", log2)
	}
	if _, err := os.Stat(filepath.Join(fx.jobDir, "statics", "q1", "slide-02.png")); !os.IsNotExist(err) {
		t.Errorf("stale slide survived: %v", err)
	}
}

func TestRenderCarouselsRefusesBadInputBeforeSpending(t *testing.T) {
	fx := newCarouselFixture(t, Width, Height)
	bad := goodCarousel()
	bad.Inputs.Slides[2] = quoteSlide("the agents never get tired")
	bad.Inputs.Slides[4] = quoteSlide(strings.Repeat("word ", 60))
	bad.Inputs.Slides[3] = section("4")
	bad.Inputs.Slides[3].Kind = "stat" // section fields under a stat template
	bad.Inputs.Slides[9].Fields["ACCENT_WORD"] = "post"
	bad.Inputs.Slides[0].Scene = ""
	short := goodCarousel()
	short.ID = "c2"
	short.Inputs.Slides = short.Inputs.Slides[:9]
	fx.opts.Illustrate = true
	_, err := RenderCarousels(fx.jobDir, &brief.Draft{Statics: []brief.Static{bad, short}}, fx.opts)
	if !errors.Is(err, ErrCarouselInput) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{
		"static c1: slide 3: quote is not a verbatim transcript span",
		"static c1: slide 5: quote is 300 characters, cap 260",
		"static c1: slide 4 (stat): missing fields STAT_LABEL_1, STAT_LABEL_2, STAT_NUMBER, STAT_UNIT",
		`static c1: slide 10: CTA accent word is "post"`,
		"static c1: slide 1 (hook): scene is empty",
		"static c2: carousel has 9 slides, want 10",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
	if _, statErr := os.Stat(fx.genLog); !os.IsNotExist(statErr) {
		t.Error("the generator ran although the input was refused")
	}

	// Without --illustrate a missing illustration is refused, not generated.
	fx.opts.Illustrate = false
	_, err = RenderCarousels(fx.jobDir, &brief.Draft{Statics: []brief.Static{goodCarousel()}}, fx.opts)
	if !errors.Is(err, ErrCarouselInput) || !strings.Contains(err.Error(), "pass --illustrate") {
		t.Fatalf("err = %v", err)
	}
}

func TestRenderCarouselsRejectsWrongSize(t *testing.T) {
	fx := newCarouselFixture(t, 1080, 1080)
	fx.opts.Illustrate = true
	_, err := RenderCarousels(fx.jobDir, &brief.Draft{Statics: []brief.Static{goodCarousel()}}, fx.opts)
	if err == nil || !strings.Contains(err.Error(), "is 1080x1080, want 1080x1350") {
		t.Fatalf("err = %v", err)
	}
}

// --- gate statics ---

func reviewed(t *testing.T, jobDir string, s *brief.Static, caption string) {
	t.Helper()
	vr := &brief.VisualReview{Reviewer: "fresh subagent", Caption: caption, Verdict: "PASS", Evidence: "robot stacks bricks beside the build-slowly line"}
	for _, f := range ListImages(jobDir, &brief.Draft{Statics: []brief.Static{*s}})[s.ID] {
		vr.Images = append(vr.Images, brief.ReviewedImage{Path: f.Path, SHA256: f.SHA256})
	}
	s.VisualReview = vr
}

func gateFixture(t *testing.T, w, h int) (*carouselFixture, *brief.Draft) {
	t.Helper()
	fx := newCarouselFixture(t, w, h)
	s := goodCarousel()
	s.Inputs.Slides = s.Inputs.Slides[:0]
	d := &brief.Draft{Statics: []brief.Static{s}}
	os.MkdirAll(filepath.Join(fx.jobDir, "statics", "c1"), 0o755)
	writePNG(t, filepath.Join(fx.jobDir, "statics", "c1", "slide-01.png"), w, h)
	writePNG(t, filepath.Join(fx.jobDir, "statics", "c1", "slide-02.png"), w, h)
	return fx, d
}

const cta = `Comment "newsletter" to get my latest post, "Chasing Cars"`

func hasField(vs []string, sub string) bool {
	for _, v := range vs {
		if strings.Contains(v, sub) {
			return true
		}
	}
	return false
}

func gateReasons(jobDir string, d *brief.Draft, opt GateOptions) []string {
	var out []string
	for _, v := range Gate(jobDir, d, opt) {
		out = append(out, v.Platform+"/"+v.Field+": "+v.Reason)
	}
	return out
}

func TestGateStaticsVisualReview(t *testing.T) {
	fx, d := gateFixture(t, Width, Height)
	caption := "We shipped 14 repos in a week.\n\n" + cta
	d.Statics[0].Caption = caption
	opt := GateOptions{CTA: cta}

	if r := gateReasons(fx.jobDir, d, opt); !hasField(r, "visual_review: missing") {
		t.Fatalf("missing review must block: %v", r)
	}
	reviewed(t, fx.jobDir, &d.Statics[0], caption)
	if r := gateReasons(fx.jobDir, d, opt); len(r) != 0 {
		t.Fatalf("a fresh PASS passes: %v", r)
	}

	// Any caption change invalidates the review.
	d.Statics[0].Caption = "Edited.\n\n" + cta
	if r := gateReasons(fx.jobDir, d, opt); !hasField(r, "stale: the caption changed") {
		t.Errorf("caption edit: %v", r)
	}
	d.Statics[0].Caption = caption
	// Any image change invalidates it too.
	writePNG(t, filepath.Join(fx.jobDir, "statics", "c1", "slide-02.png"), Width, Height+0)
	os.WriteFile(filepath.Join(fx.jobDir, "statics", "c1", "slide-02.png"), append(mustRead(t, filepath.Join(fx.jobDir, "statics", "c1", "slide-02.png")), 0), 0o644)
	if r := gateReasons(fx.jobDir, d, opt); !hasField(r, "slide-02.png changed after the review") {
		t.Errorf("image edit: %v", r)
	}
	reviewed(t, fx.jobDir, &d.Statics[0], caption)
	writePNG(t, filepath.Join(fx.jobDir, "statics", "c1", "slide-03.png"), Width, Height)
	if r := gateReasons(fx.jobDir, d, opt); !hasField(r, "slide-03.png was not reviewed") {
		t.Errorf("new image: %v", r)
	}
	os.Remove(filepath.Join(fx.jobDir, "statics", "c1", "slide-03.png"))

	d.Statics[0].VisualReview.Verdict = "FAIL"
	if r := gateReasons(fx.jobDir, d, opt); !hasField(r, `verdict is "FAIL"`) {
		t.Errorf("failed review: %v", r)
	}
	d.Statics[0].VisualReview.Verdict = "PASS"

	// The canonical CTA must end every caption.
	d.Statics[0].Caption = "No CTA here"
	reviewed(t, fx.jobDir, &d.Statics[0], d.Statics[0].Caption)
	if r := gateReasons(fx.jobDir, d, opt); !hasField(r, "instagram/caption: caption does not end with the canonical newsletter CTA") || !hasField(r, "linkedin/caption") {
		t.Errorf("CTA: %v", r)
	}
	if r := gateReasons(fx.jobDir, d, GateOptions{}); len(r) != 0 {
		t.Errorf("without newsletter-canon the CTA is not checked: %v", r)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestGateStaticsInstagramAspectAndSchedule(t *testing.T) {
	fx, d := gateFixture(t, 1080, 1500) // 0.72: too tall for Instagram
	reviewed(t, fx.jobDir, &d.Statics[0], d.Statics[0].Caption)
	if r := gateReasons(fx.jobDir, d, GateOptions{}); !hasField(r, "instagram/images: statics/c1/slide-01.png is 1080x1500") {
		t.Fatalf("aspect: %v", r)
	}
	d.Statics[0].Platforms = []string{"linkedin"}
	if r := gateReasons(fx.jobDir, d, GateOptions{}); len(r) != 0 {
		t.Fatalf("other platforms take any aspect: %v", r)
	}

	// Approved: the caption about to post (schedule.json) must be the reviewed one.
	sch := brief.Schedule{SchemaVersion: "1", Cells: []brief.ScheduleCell{{Kind: "static", ClipID: "c1", Platform: "linkedin", Title: "edited after approval"}}}
	b, _ := json.Marshal(sch)
	os.MkdirAll(filepath.Join(fx.jobDir, "review"), 0o755)
	os.WriteFile(filepath.Join(fx.jobDir, "review", "schedule.json"), b, 0o644)
	if r := gateReasons(fx.jobDir, d, GateOptions{Schedule: true}); !hasField(r, "linkedin/visual_review: stale: the caption about to post") {
		t.Fatalf("schedule caption: %v", r)
	}
	sch.Cells = nil
	b, _ = json.Marshal(sch)
	os.WriteFile(filepath.Join(fx.jobDir, "review", "schedule.json"), b, 0o644)
	if r := gateReasons(fx.jobDir, d, GateOptions{Schedule: true}); len(r) != 0 {
		t.Fatalf("held statics are not checked: %v", r)
	}
}

func TestGateStaticsHostedBytes(t *testing.T) {
	fx, d := gateFixture(t, Width, Height)
	reviewed(t, fx.jobDir, &d.Statics[0], d.Statics[0].Caption)
	files := map[string][]byte{
		"/slide-01.png": mustRead(t, filepath.Join(fx.jobDir, "statics", "c1", "slide-01.png")),
		"/slide-02.png": []byte("recompressed by the host"),
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	d.Statics[0].VisualReview.Images[0].URL = srv.URL + "/slide-01.png"
	d.Statics[0].VisualReview.Images[1].URL = srv.URL + "/slide-02.png"
	opt := GateOptions{Hosted: true, Client: srv.Client()}
	r := gateReasons(fx.jobDir, d, opt)
	if len(r) != 1 || !strings.Contains(r[0], "slide-02.png bytes differ from the reviewed") {
		t.Fatalf("hosted: %v", r)
	}
	files["/slide-02.png"] = mustRead(t, filepath.Join(fx.jobDir, "statics", "c1", "slide-02.png"))
	if r := gateReasons(fx.jobDir, d, opt); len(r) != 0 {
		t.Fatalf("identical hosted bytes pass: %v", r)
	}
	d.Statics[0].VisualReview.Images[1].URL = ""
	if r := gateReasons(fx.jobDir, d, opt); !hasField(r, "has no https url") {
		t.Fatalf("unhosted: %v", r)
	}
	if r := gateReasons(fx.jobDir, d, GateOptions{}); len(r) != 0 {
		t.Fatalf("without --hosted URLs are not needed: %v", r)
	}
}
