package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/opusclip"
)

func init() {
	Register(Command{Name: "wait", Short: "Poll OpusClip until both projects have clips, write clips.json (uploaded -> clipped)", Run: runWait})
}

// ClipsFile is clips.json: the summary shape the publish skill reads.
// uri_preview / uri_export are signed GCS URLs that expire; FetchedAt says
// when they were read, and `longform opus export` re-fetches fresh ones.
type ClipsFile struct {
	SchemaVersion   string        `json:"schema_version"`
	ShortsProjectID string        `json:"shorts_project_id"`
	FullProjectID   string        `json:"full_project_id"`
	FullClipID      string        `json:"full_clip_id"`
	FetchedAt       string        `json:"fetched_at"`
	URIsExpire      bool          `json:"uris_expire"`
	Clips           []ClipSummary `json:"clips"`
}

// ClipSummary mirrors the fields the vendor CLI's list/--summary modes emit.
type ClipSummary struct {
	ProjectID       string          `json:"project_id"`
	ClipID          string          `json:"clip_id"`
	Rank            int             `json:"rank"`
	Title           string          `json:"title"`
	Description     string          `json:"description"`
	Hashtags        []string        `json:"hashtags"`
	Score           float64         `json:"score"`
	DurationSec     int             `json:"duration_sec"`
	URIPreview      string          `json:"uri_preview"`
	URIExport       string          `json:"uri_export"`
	URIThumbnail    string          `json:"uri_thumbnail,omitempty"`
	IsBonus         bool            `json:"is_bonus"`
	IsFull          bool            `json:"is_full"`
	HookScore       float64         `json:"hook_score,omitempty"`
	CoherenceScore  float64         `json:"coherence_score,omitempty"`
	ConnectionScore float64         `json:"connection_score,omitempty"`
	TrendScore      float64         `json:"trend_score,omitempty"`
	Raw             json.RawMessage `json:"raw"`
}

// TranscriptsFile is analysis/opus-transcripts.json.
type TranscriptsFile struct {
	SchemaVersion string           `json:"schema_version"`
	Clips         []ClipTranscript `json:"clips"`
}

// ClipTranscript is one clip's transcript from the describe endpoint:
// Transcript is verbatim (one string, literal "__silence" tokens between
// phrases, no timestamps); TranscriptClean has the tokens removed.
type ClipTranscript struct {
	ProjectID       string `json:"project_id"`
	ClipID          string `json:"clip_id"`
	Title           string `json:"title"`
	Transcript      string `json:"transcript"`
	TranscriptClean string `json:"transcript_clean"`
}

// stripPace is the delay between overlay-strip re-render submissions.
var stripPace = 3 * time.Second

// renderPollInterval is how often WaitRender re-describes a clip.
var renderPollInterval = 15 * time.Second

func runWait(args []string) int {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	interval := fs.Duration("interval", 270*time.Second, "poll interval")
	ceiling := fs.Duration("ceiling", 2*time.Hour, "give up (exit 75) after this long")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: longform wait <jobdir> [--interval 270s] [--ceiling 2h]")
		return 64
	}
	job, err := model.LoadJob(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "wait:", err)
		return 65
	}
	if job.AtLeast(model.StateClipped) {
		fmt.Printf("wait: %s already %s\n", job.ID, job.State)
		return 0
	}
	if job.State != model.StateUploaded || job.OpusClip.ShortsProjectID == "" || job.OpusClip.FullProjectID == "" {
		fmt.Fprintf(os.Stderr, "wait: job %s is %s with shorts=%q full=%q; need uploaded with both project ids\n", job.ID, job.State, job.OpusClip.ShortsProjectID, job.OpusClip.FullProjectID)
		return 65
	}
	profile, err := model.LoadProfile(job.Profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "wait:", err)
		return 65
	}
	lock, err := model.Acquire(job.Dir(), "wait")
	if err != nil {
		fmt.Fprintln(os.Stderr, "wait:", err)
		return 75
	}
	defer lock.Release()
	client, err := newOpusClient()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wait:", err)
		return 65
	}
	ctx, cancel := signalContext()
	defer cancel()
	ctx, cancelT := context.WithTimeout(ctx, *ceiling)
	defer cancelT()

	err = waitJob(ctx, client, job, profile, *interval)
	switch {
	case err == nil:
		fmt.Printf("wait: %s clipped (%s)\n", job.ID, job.Artifacts.Clips)
		return 0
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintf(os.Stderr, "wait: ceiling %s reached for %s; state unchanged (%s)\n", *ceiling, job.ID, job.State)
		return 75
	case errors.Is(err, context.Canceled):
		fmt.Fprintln(os.Stderr, "wait: interrupted; state unchanged")
		return 75
	default:
		fmt.Fprintln(os.Stderr, "wait:", err)
		var ae *opusclip.APIError
		if errors.As(err, &ae) && ae.Temporary() {
			return 75
		}
		return 70
	}
}

// waitJob polls until both projects are done curating, then strips overlays
// (if the profile asks and the clips carry one), writes clips.json and the
// transcripts, and advances to clipped. Any ctx error is returned untouched
// so the caller can map it to exit 75 without changing state.
//
// Completion rule: the list endpoint carries no project stage, and a
// curation project's clip count grows while it is still processing (probed
// 2026-10-21: first clip at +237 s), so a project counts as done only when
// two consecutive polls return the same non-zero clip count.
func waitJob(ctx context.Context, client *opusclip.Client, job *model.Job, profile *model.Profile, interval time.Duration) error {
	shortsID, fullID := job.OpusClip.ShortsProjectID, job.OpusClip.FullProjectID
	var shorts, full []opusclip.Clip
	prevShorts, prevFull := -1, -1
	for {
		var err error
		shorts, err = client.ListClips(ctx, shortsID)
		if err != nil {
			return fmt.Errorf("list shorts clips: %w", err)
		}
		full, err = client.ListClips(ctx, fullID)
		if err != nil {
			return fmt.Errorf("list full clips: %w", err)
		}
		stable := len(shorts) >= 1 && len(full) >= 1 && len(shorts) == prevShorts && len(full) == prevFull
		if stable {
			break
		}
		fmt.Fprintf(os.Stderr, "wait: shorts=%d full=%d clips (prev %d/%d); next poll in %s\n", len(shorts), len(full), prevShorts, prevFull, interval)
		prevShorts, prevFull = len(shorts), len(full)
		if err := sleepCtx(ctx, interval); err != nil {
			return err
		}
	}

	all := append(append([]opusclip.Clip{}, shorts...), full...)

	// Overlay strip: submit once per clip across both projects, 3 s apart,
	// then wait for every render to settle. Clips whose describe payload
	// shows no screen overlay are skipped (the account's only template has
	// screenOverlays: []). The submitted flag is persisted so a re-run after
	// a ceiling never re-submits.
	if profile.OpusClip.StripOverlay && !job.OpusClip.OverlayStripped {
		submitted := 0
		for i, cl := range all {
			if i > 0 {
				if err := sleepCtx(ctx, stripPace); err != nil {
					return err
				}
			}
			if _, err := client.StripOverlay(ctx, cl.ProjectID(), cl.ClipID(), false); err != nil {
				if errors.Is(err, opusclip.ErrNoEditingScript) || errors.Is(err, opusclip.ErrNoOverlay) {
					fmt.Fprintf(os.Stderr, "wait: %s: %v (skipped)\n", cl.ID, err)
					continue
				}
				return fmt.Errorf("strip overlay %s: %w", cl.ID, err)
			}
			submitted++
		}
		job.OpusClip.OverlayStripped = true
		setExtra(job, "opusclip_overlay_strips_submitted", submitted)
		if err := job.Save(); err != nil {
			return err
		}
	}
	if profile.OpusClip.StripOverlay {
		for _, cl := range all {
			if _, err := client.WaitRender(ctx, cl.ProjectID(), cl.ClipID(), renderPollInterval); err != nil {
				return fmt.Errorf("wait render %s: %w", cl.ID, err)
			}
		}
		// Re-list so clips.json carries the post-strip objects.
		var err error
		if shorts, err = client.ListClips(ctx, shortsID); err != nil {
			return err
		}
		if full, err = client.ListClips(ctx, fullID); err != nil {
			return err
		}
	}

	cf := ClipsFile{SchemaVersion: "1", ShortsProjectID: shortsID, FullProjectID: fullID, FullClipID: full[0].ClipID(), FetchedAt: time.Now().UTC().Format(time.RFC3339), URIsExpire: true}
	for _, cl := range shorts {
		cf.Clips = append(cf.Clips, summarize(cl, false))
	}
	for _, cl := range full {
		cf.Clips = append(cf.Clips, summarize(cl, true))
	}
	b, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return err
	}
	if err := model.WriteFileAtomic(job.Path("clips.json"), b, 0o644); err != nil {
		return err
	}

	tf := TranscriptsFile{SchemaVersion: "1"}
	for _, cl := range cf.Clips {
		d, err := client.DescribeClip(ctx, cl.ProjectID, cl.ClipID, "")
		if err != nil {
			return fmt.Errorf("describe %s.%s: %w", cl.ProjectID, cl.ClipID, err)
		}
		tr := d.Transcript()
		tf.Clips = append(tf.Clips, ClipTranscript{ProjectID: cl.ProjectID, ClipID: cl.ClipID, Title: d.Title, Transcript: tr, TranscriptClean: opusclip.CleanTranscript(tr)})
	}
	b, err = json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return err
	}
	if err := model.WriteFileAtomic(job.Path("analysis/opus-transcripts.json"), b, 0o644); err != nil {
		return err
	}
	job.OpusClip.FullClipID = cf.FullClipID
	job.Artifacts.Clips = "clips.json"
	job.Artifacts.OpusTranscripts = "analysis/opus-transcripts.json"
	return job.Advance(model.StateClipped, "wait", fmt.Sprintf("%d shorts, %d full", len(shorts), len(full)))
}

func summarize(cl opusclip.Clip, isFull bool) ClipSummary {
	s := ClipSummary{
		ProjectID:    cl.ProjectID(),
		ClipID:       cl.ClipID(),
		Rank:         cl.Rank,
		Title:        cl.Title,
		Description:  cl.Description,
		Hashtags:     []string(cl.Hashtags),
		Score:        cl.Score,
		DurationSec:  cl.DurationSec(),
		URIPreview:   cl.URIForPreview,
		URIExport:    cl.URIForExport,
		URIThumbnail: cl.URIForThumbnail,
		IsBonus:      cl.IsBonusClip,
		IsFull:       isFull,
		Raw:          cl.Raw,
	}
	if s.Hashtags == nil {
		s.Hashtags = []string{}
	}
	if cl.JudgeResult != nil {
		s.HookScore, s.CoherenceScore, s.ConnectionScore, s.TrendScore = cl.JudgeResult.HookScore, cl.JudgeResult.CoherenceScore, cl.JudgeResult.ConnectionScore, cl.JudgeResult.TrendScore
	}
	return s
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
