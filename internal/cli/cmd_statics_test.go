package cli

import (
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaellady/longform-pipeline/internal/brief"
	"github.com/michaellady/longform-pipeline/internal/model"
)

// TestGateStaticsAndNewsletterCTA drives `longform brief newsletter-cta`
// and `longform gate statics` end to end on a drafted job: the CTA comes
// from cta.sh with the newest newsletter title, a static without a visual
// review blocks (65), and one reviewed against its exact caption and bytes
// passes (0).
func TestGateStaticsAndNewsletterCTA(t *testing.T) {
	t.Setenv("LONGFORM_CONFIG_DIR", t.TempDir())
	t.Setenv("LONGFORM_DATA_DIR", t.TempDir())
	skills := t.TempDir()
	os.MkdirAll(filepath.Join(skills, "_shared"), 0o755)
	os.WriteFile(filepath.Join(skills, "_shared", "cta.sh"), []byte("#!/usr/bin/env bash\nprintf 'Comment \"newsletter\" to get my latest post, \"%s\"' \"$1\"\n"), 0o755)
	corpus := filepath.Join(skills, "cache.json")
	os.WriteFile(corpus, []byte(`{"posts":[{"title":"Chasing Cars","published_at":"2026-11-08","source_type":"newsletter"},{"title":"Old","published_at":"2026-10-01","source_type":"newsletter"}]}`), 0o644)
	os.MkdirAll(model.ProfilesDir(), 0o755)
	prof := map[string]any{"name": "evc", "inbox": "/tmp/in",
		"statics": map[string]any{"engine": "custom-html", "formats": []string{"carousel"}},
		"copy":    map[string]any{"voice_corpus": corpus},
		"cta":     map[string]any{"static_cta": "newsletter-canon", "cta_script": filepath.Join(skills, "_shared", "cta.sh")}}
	b, _ := json.Marshal(prof)
	os.WriteFile(model.ProfilePath("evc"), b, 0o644)

	job, err := model.NewJob("evc", strings.Repeat("d", 64), nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []model.State{model.StateStaged, model.StatePrepared, model.StateUploaded, model.StateClipped} {
		job.Advance(s, "test", "")
	}
	cta := `Comment "newsletter" to get my latest post, "Chasing Cars"`
	out, code := captureStdout(t, func() int { return Run([]string{"brief", "newsletter-cta", "--profile", "evc"}) })
	if code != 0 || strings.TrimSpace(out) != cta {
		t.Fatalf("newsletter-cta = %d %q", code, out)
	}

	os.MkdirAll(job.Path("statics/six"), 0o755)
	f, _ := os.Create(job.Path("statics/six/slide-01.png"))
	png.Encode(f, image.NewRGBA(image.Rect(0, 0, 1080, 1350)))
	f.Close()
	caption := "Six stops on the way to shipping.\n\n" + cta
	d := brief.Draft{SchemaVersion: "1", JobID: job.ID, Profile: "evc", Statics: []brief.Static{{ID: "six", Format: "carousel", Platforms: []string{"instagram"}, Caption: caption}}}
	write := func() {
		b, _ := json.Marshal(d)
		os.WriteFile(job.Path(brief.DraftFile), b, 0o644)
	}
	write()
	out, code = captureStdout(t, func() int { return Run([]string{"gate", "statics", "--job", job.Dir()}) })
	if code != 65 || !strings.Contains(out, `"field":"visual_review"`) {
		t.Fatalf("missing review: exit %d\n%s", code, out)
	}
	var listed map[string][]struct{ Path, SHA256 string }
	out, _ = captureStdout(t, func() int { return Run([]string{"gate", "statics", "--job", job.Dir(), "--list"}) })
	if err := json.Unmarshal([]byte(out), &listed); err != nil || len(listed["six"]) != 1 {
		t.Fatalf("--list: %v %s", err, out)
	}
	d.Statics[0].VisualReview = &brief.VisualReview{Reviewer: "fresh subagent", Caption: caption, Verdict: "PASS", Evidence: "six numbered stops match the caption",
		Images: []brief.ReviewedImage{{Path: listed["six"][0].Path, SHA256: listed["six"][0].SHA256}}}
	write()
	out, code = captureStdout(t, func() int { return Run([]string{"gate", "statics", "--job", job.Dir()}) })
	if code != 0 {
		t.Fatalf("reviewed static: exit %d\n%s", code, out)
	}
}
