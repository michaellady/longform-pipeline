package statics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/michaellady/longform-pipeline/internal/brief"
	"github.com/michaellady/longform-pipeline/internal/gates"
)

// Instagram's accepted image aspect range (width / height), the guard the
// newsletter skills apply before posting.
const (
	InstagramMinAspect = 0.8
	InstagramMaxAspect = 1.91
)

// GateOptions configures `longform gate statics`.
type GateOptions struct {
	// CTA is the canonical newsletter CTA every caption must end with; ""
	// skips the check (profile without cta.static_cta newsletter-canon).
	CTA string
	// Hosted fetches every reviewed image URL and compares its sha256 with
	// the reviewed bytes.
	Hosted bool
	Client *http.Client
	// Schedule checks the static cells of review/schedule.json (the job is
	// approved) instead of every static in the draft.
	Schedule bool
}

// StaticImages lists a static's rendered PNGs, job-relative: the PNG files
// of statics/<id>/index.json when present, else every statics/<id>/*.png.
func StaticImages(jobDir, id string) []string {
	dir := filepath.Join(jobDir, "statics", id)
	var names []string
	if b, err := os.ReadFile(filepath.Join(dir, "index.json")); err == nil {
		var ix Result
		if json.Unmarshal(b, &ix) == nil {
			for _, f := range ix.Files {
				if strings.HasSuffix(strings.ToLower(f), ".png") {
					names = append(names, f)
				}
			}
		}
	}
	if len(names) == 0 {
		m, _ := filepath.Glob(filepath.Join(dir, "*.png"))
		for _, f := range m {
			names = append(names, filepath.Base(f))
		}
	}
	sort.Strings(names)
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = filepath.ToSlash(filepath.Join("statics", id, n))
	}
	return out
}

// ListImages returns every static's rendered PNGs with their digests and
// sizes: what the skill copies into visual_review.images.
func ListImages(jobDir string, d *brief.Draft) map[string][]RenderedFile {
	out := map[string][]RenderedFile{}
	for _, s := range d.Statics {
		files := []RenderedFile{}
		for _, rel := range StaticImages(jobDir, s.ID) {
			p := filepath.Join(jobDir, filepath.FromSlash(rel))
			sum, err := FileSHA256(p)
			if err != nil {
				continue
			}
			w, h, _ := PNGSize(p)
			files = append(files, RenderedFile{Path: rel, SHA256: sum, Width: w, Height: h})
		}
		out[s.ID] = files
	}
	return out
}

// Gate checks every static the job will post (the static cells of
// review/schedule.json with opt.Schedule, else every static in the draft): a
// rendered image set, a PASS visual review bound to the exact caption and
// the current image bytes, captions in schedule.json unchanged since the
// review, the Instagram aspect range, the canonical CTA ending and, with
// Hosted, hosted bytes identical to the reviewed ones. Verdicts use the
// gate's exit-65 shape.
func Gate(jobDir string, d *brief.Draft, opt GateOptions) []gates.Verdict {
	type target struct {
		s        brief.Static
		captions map[string]string // platform -> caption about to post
	}
	byID := map[string]brief.Static{}
	for _, s := range d.Statics {
		byID[s.ID] = s
	}
	var targets []target
	var out []gates.Verdict
	sch, schErr := (*brief.Schedule)(nil), error(nil)
	if opt.Schedule {
		sch, schErr = loadSchedule(jobDir)
		if schErr != nil {
			return []gates.Verdict{verdict("", "", "schedule", schErr.Error(), "run brief parse-decision first")}
		}
	}
	if sch != nil {
		seen := map[string]int{}
		for _, c := range sch.Cells {
			if c.Kind != "static" {
				continue
			}
			s, ok := byID[c.ClipID]
			if !ok {
				out = append(out, verdict(c.ClipID, c.Platform, "static", "schedule.json names a static the draft does not have", "re-run brief parse-decision"))
				continue
			}
			i, ok := seen[c.ClipID]
			if !ok {
				i = len(targets)
				seen[c.ClipID] = i
				targets = append(targets, target{s: s, captions: map[string]string{}})
			}
			targets[i].captions[c.Platform] = c.Title
		}
	} else {
		for _, s := range d.Statics {
			caps := map[string]string{}
			for _, p := range s.Platforms {
				caps[p] = s.Caption
			}
			targets = append(targets, target{s: s, captions: caps})
		}
	}

	client := opt.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	for _, t := range targets {
		s := t.s
		images := StaticImages(jobDir, s.ID)
		if len(images) == 0 {
			out = append(out, verdict(s.ID, "", "images", "no rendered image in statics/"+s.ID+"/", "render it (brief render-carousel or render-statics)"))
			continue
		}
		if opt.CTA != "" {
			for _, p := range sortedKeys(t.captions) {
				if !strings.HasSuffix(strings.TrimRight(t.captions[p], " \t\r\n"), opt.CTA) {
					out = append(out, verdict(s.ID, p, "caption", "caption does not end with the canonical newsletter CTA", "end the caption with exactly: "+opt.CTA))
				}
			}
		}
		for _, p := range sortedKeys(t.captions) {
			if p != "instagram" {
				continue
			}
			for _, rel := range images {
				w, h, err := PNGSize(filepath.Join(jobDir, filepath.FromSlash(rel)))
				if err != nil || h == 0 {
					out = append(out, verdict(s.ID, p, "images", fmt.Sprintf("%s: cannot read dimensions: %v", rel, err), ""))
					continue
				}
				if r := float64(w) / float64(h); r < InstagramMinAspect || r > InstagramMaxAspect {
					out = append(out, verdict(s.ID, p, "images", fmt.Sprintf("%s is %dx%d (aspect %.3f), outside Instagram's %.2f..%.2f", rel, w, h, r, InstagramMinAspect, InstagramMaxAspect), "re-render at 4:5 (1080x1350) or hold the Instagram cell"))
				}
			}
		}
		vr := s.VisualReview
		if vr == nil {
			out = append(out, verdict(s.ID, "", "visual_review", "missing: no independent visual review of the final images beside the caption", "have a fresh image-capable reviewer open every PNG with the exact caption and the transcript passage, then record visual_review"))
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(vr.Verdict), "PASS") {
			out = append(out, verdict(s.ID, "", "visual_review", fmt.Sprintf("verdict is %q, not PASS: %s", vr.Verdict, vr.Evidence), "fix the image or the caption and review again, or hold the static"))
			continue
		}
		if strings.TrimSpace(vr.Reviewer) == "" || strings.TrimSpace(vr.Evidence) == "" {
			out = append(out, verdict(s.ID, "", "visual_review", "reviewer and evidence are required", "record who reviewed and what they saw in the images"))
		}
		if vr.Caption != s.Caption {
			out = append(out, verdict(s.ID, "", "visual_review", "stale: the caption changed after the review", "review the images again beside the new caption"))
		}
		for _, p := range sortedKeys(t.captions) {
			if t.captions[p] != vr.Caption {
				out = append(out, verdict(s.ID, p, "visual_review", "stale: the caption about to post differs from the reviewed caption", "review again beside the caption in review/schedule.json, or restore it"))
			}
		}
		reviewed := map[string]brief.ReviewedImage{}
		for _, im := range vr.Images {
			reviewed[filepath.ToSlash(filepath.Clean(im.Path))] = im
		}
		for _, rel := range images {
			im, ok := reviewed[rel]
			if !ok {
				out = append(out, verdict(s.ID, "", "visual_review", "stale: "+rel+" was not reviewed", "review every final image"))
				continue
			}
			delete(reviewed, rel)
			sum, err := FileSHA256(filepath.Join(jobDir, filepath.FromSlash(rel)))
			if err != nil {
				out = append(out, verdict(s.ID, "", "images", err.Error(), ""))
				continue
			}
			if !strings.EqualFold(sum, im.SHA256) {
				out = append(out, verdict(s.ID, "", "visual_review", "stale: "+rel+" changed after the review (sha256 differs)", "review the new image"))
				continue
			}
			if opt.Hosted {
				if v, ok := checkHosted(client, s.ID, rel, im); !ok {
					out = append(out, v)
				}
			}
		}
		for _, rel := range sortedKeys(reviewed) {
			out = append(out, verdict(s.ID, "", "visual_review", "stale: reviewed "+rel+" is no longer a rendered image", "review the current image set"))
		}
	}
	return out
}

func checkHosted(client *http.Client, id, rel string, im brief.ReviewedImage) (gates.Verdict, bool) {
	if !strings.HasPrefix(im.URL, "https://") {
		return verdict(id, "", "visual_review", "hosted: "+rel+" has no https url", "host the reviewed PNG and record its public URL in visual_review.images[].url"), false
	}
	resp, err := client.Get(im.URL)
	if err != nil {
		return verdict(id, "", "visual_review", fmt.Sprintf("hosted: %s: %v", im.URL, err), "check the URL is public"), false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return verdict(id, "", "visual_review", fmt.Sprintf("hosted: %s answered %d", im.URL, resp.StatusCode), "check the URL is public and pinned to the pushed commit"), false
	}
	h := sha256.New()
	if _, err := io.Copy(h, resp.Body); err != nil {
		return verdict(id, "", "visual_review", fmt.Sprintf("hosted: %s: %v", im.URL, err), ""), false
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, im.SHA256) {
		return verdict(id, "", "visual_review", fmt.Sprintf("hosted: %s bytes differ from the reviewed %s (sha256 %s, reviewed %s)", im.URL, rel, got, im.SHA256), "host the exact reviewed file, or review the hosted image"), false
	}
	return gates.Verdict{}, true
}

func verdict(id, platform, field, reason, hint string) gates.Verdict {
	return gates.Verdict{ClipID: id, Label: "static " + id, Platform: platform, Field: field, Reason: reason, FixHint: hint}
}

func loadSchedule(jobDir string) (*brief.Schedule, error) {
	b, err := os.ReadFile(filepath.Join(jobDir, filepath.FromSlash(brief.ScheduleFile)))
	if err != nil {
		return nil, err
	}
	var s brief.Schedule
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", brief.ScheduleFile, err)
	}
	return &s, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
