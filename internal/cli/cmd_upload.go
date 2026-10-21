package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/opusclip"
)

// newOpusClient builds the API client. Tests swap it to inject a client with
// no rate limit and short backoff against httptest.
var newOpusClient = func() (*opusclip.Client, error) {
	return opusclip.NewFromEnv(opusclip.WithLogger(log.New(os.Stderr, "", log.LstdFlags)))
}

// job.Extra keys the upload step uses to make the 4-step upload resumable
// across runs without touching the shared model.
const (
	extraUploadURL     = "opusclip_upload_url"     // signed GCS url from step 1
	extraUploadSession = "opusclip_upload_session" // resumable session Location from step 2
	extraUploadDone    = "opusclip_upload_done"    // true once step 3 completed
)

func init() {
	Register(Command{Name: "upload", Short: "Upload delivery.mp4 to OpusClip and create the shorts + full projects (prepared -> uploaded)", Run: runUpload})
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func runUpload(args []string) int {
	fs := flag.NewFlagSet("upload", flag.ContinueOnError)
	timeout := fs.Duration("timeout", 3*time.Hour, "overall deadline for the upload")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: longform upload <jobdir> [--timeout 3h]")
		return 64
	}
	job, err := model.LoadJob(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "upload:", err)
		return 65
	}
	if job.AtLeast(model.StateUploaded) && job.OpusClip.ShortsProjectID != "" && job.OpusClip.FullProjectID != "" {
		fmt.Printf("upload: %s already uploaded (shorts=%s full=%s)\n", job.ID, job.OpusClip.ShortsProjectID, job.OpusClip.FullProjectID)
		return 0
	}
	if job.State != model.StatePrepared && job.State != model.StateUploaded {
		fmt.Fprintf(os.Stderr, "upload: job %s is %s, need prepared\n", job.ID, job.State)
		return 65
	}
	profile, err := model.LoadProfile(job.Profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "upload:", err)
		return 65
	}
	delivery := job.Artifacts.Delivery
	if delivery == "" {
		delivery = "delivery.mp4"
	}
	deliveryPath := job.Path(delivery)
	if _, err := os.Stat(deliveryPath); err != nil {
		fmt.Fprintln(os.Stderr, "upload: delivery missing:", err)
		return 65
	}
	lock, err := model.Acquire(job.Dir(), "upload")
	if err != nil {
		fmt.Fprintln(os.Stderr, "upload:", err)
		return 75
	}
	defer lock.Release()

	client, err := newOpusClient()
	if err != nil {
		fmt.Fprintln(os.Stderr, "upload:", err)
		return 65
	}
	ctx, cancel := signalContext()
	defer cancel()
	ctx, cancelT := context.WithTimeout(ctx, *timeout)
	defer cancelT()

	if err := uploadJob(ctx, client, job, profile, deliveryPath); err != nil {
		fmt.Fprintln(os.Stderr, "upload:", err)
		var ae *opusclip.APIError
		if errors.As(err, &ae) && ae.Temporary() || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return 75
		}
		return 70
	}
	fmt.Printf("upload: %s uploaded (upload=%s shorts=%s full=%s)\n", job.ID, job.OpusClip.UploadID, job.OpusClip.ShortsProjectID, job.OpusClip.FullProjectID)
	return 0
}

func extraString(j *model.Job, k string) string {
	if j.Extra == nil {
		return ""
	}
	s, _ := j.Extra[k].(string)
	return s
}

func setExtra(j *model.Job, k string, v any) {
	if j.Extra == nil {
		j.Extra = map[string]any{}
	}
	j.Extra[k] = v
}

// uploadJob is the idempotent core: every step records its result in
// job.json before the next step starts, so a re-run resumes where it stopped
// and never re-uploads or re-creates a project.
func uploadJob(ctx context.Context, client *opusclip.Client, job *model.Job, profile *model.Profile, deliveryPath string) error {
	// Step 1: upload link (recorded immediately so the id is reused).
	if job.OpusClip.UploadID == "" {
		ul, err := client.CreateUploadLink(ctx)
		if err != nil {
			return fmt.Errorf("upload link: %w", err)
		}
		job.OpusClip.UploadID = ul.UploadID
		setExtra(job, extraUploadURL, ul.URL)
		setExtra(job, extraUploadSession, "")
		setExtra(job, extraUploadDone, false)
		if err := job.Save(); err != nil {
			return err
		}
	}
	done, _ := job.Extra[extraUploadDone].(bool)
	if !done {
		// Step 2: resumable session (reused on re-run so a partial PUT resumes).
		sess := extraString(job, extraUploadSession)
		if sess == "" {
			signed := extraString(job, extraUploadURL)
			if signed == "" {
				return fmt.Errorf("job has uploadId %s but no upload url recorded; clear opusclip.upload_id to start over", job.OpusClip.UploadID)
			}
			var err error
			sess, err = client.StartResumableSession(ctx, signed)
			if err != nil {
				return fmt.Errorf("resumable session: %w", err)
			}
			setExtra(job, extraUploadSession, sess)
			if err := job.Save(); err != nil {
				return err
			}
		}
		// Step 3: stream the bytes.
		if err := client.PutFile(ctx, sess, deliveryPath); err != nil {
			return fmt.Errorf("put file: %w", err)
		}
		setExtra(job, extraUploadDone, true)
		if err := job.Save(); err != nil {
			return err
		}
	}
	// Step 4: the two projects, each recorded the moment it exists.
	if job.OpusClip.ShortsProjectID == "" {
		p, err := client.CreateProject(ctx, shortsProjectRequest(job, profile))
		if err != nil {
			return fmt.Errorf("shorts project: %w", err)
		}
		job.OpusClip.ShortsProjectID = p.ProjectID
		recordProject(job, "shorts", p)
		if err := job.Save(); err != nil {
			return err
		}
	}
	if job.OpusClip.FullProjectID == "" {
		p, err := client.CreateProject(ctx, fullProjectRequest(job, profile))
		if err != nil {
			return fmt.Errorf("full project: %w", err)
		}
		job.OpusClip.FullProjectID = p.ProjectID
		recordProject(job, "full", p)
		if err := job.Save(); err != nil {
			return err
		}
	}
	return job.Advance(model.StateUploaded, "upload", "")
}

// job.Extra keys for the running credit estimate. There is no REST usage
// endpoint (probed 2026-10-21: /usage, /credits, /me, /user all 404), so the
// watcher records sourceInfo.durationMs of every project it creates and
// `longform opus usage` sums ceil(minutes) across jobs.
const (
	extraProjects        = "opusclip_projects"         // []{id, kind, stage, title, source_duration_ms, credits_estimate, created_at}
	extraCreditsEstimate = "opusclip_credits_estimate" // sum of credits_estimate for this job
)

// ProjectRecord is one entry of job.Extra["opusclip_projects"].
type ProjectRecord struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"` // shorts | full
	Stage            string `json:"stage,omitempty"`
	Title            string `json:"title,omitempty"`
	SourceDurationMs int64  `json:"source_duration_ms"`
	CreditsEstimate  int    `json:"credits_estimate"`
	CreatedAt        string `json:"created_at"`
}

func recordProject(job *model.Job, kind string, p *opusclip.Project) {
	recs := projectRecords(job)
	recs = append(recs, ProjectRecord{ID: p.ProjectID, Kind: kind, Stage: p.Stage, Title: p.Title, SourceDurationMs: p.SourceDurationMs, CreditsEstimate: p.CreditEstimate(), CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	total := 0
	var generic []any
	for _, r := range recs {
		total += r.CreditsEstimate
		b, _ := json.Marshal(r)
		var m map[string]any
		json.Unmarshal(b, &m)
		generic = append(generic, m)
	}
	setExtra(job, extraProjects, generic)
	setExtra(job, extraCreditsEstimate, total)
}

// projectRecords decodes job.Extra["opusclip_projects"] (tolerating the
// map[string]any form it takes after a JSON round trip).
func projectRecords(job *model.Job) []ProjectRecord {
	if job.Extra == nil {
		return nil
	}
	b, err := json.Marshal(job.Extra[extraProjects])
	if err != nil {
		return nil
	}
	var recs []ProjectRecord
	if json.Unmarshal(b, &recs) != nil {
		return nil
	}
	return recs
}

func shortsProjectRequest(job *model.Job, profile *model.Profile) opusclip.CreateProjectRequest {
	s := profile.OpusClip.Shorts
	cur := &opusclip.CurationPref{Model: s.Model, ClipDurations: opusclip.Durations(s.Durations)}
	if s.Model == "ClipAnything" && s.Prompt != "" {
		cur.CustomPrompt = s.Prompt
	}
	render := &opusclip.RenderPref{LayoutAspectRatio: s.Aspect}
	if s.RemoveFiller {
		render.QuickstartConfig = &opusclip.QuickstartConfig{EnableRemoveFillerWords: true}
	}
	return opusclip.CreateProjectRequest{
		VideoURL:          job.OpusClip.UploadID,
		BrandTemplateID:   profile.OpusClip.TemplateID,
		UploadedVideoAttr: &opusclip.UploadedVideoAttr{Title: job.Slug},
		CurationPref:      cur,
		RenderPref:        render,
	}
}

func fullProjectRequest(job *model.Job, profile *model.Profile) opusclip.CreateProjectRequest {
	return opusclip.CreateProjectRequest{
		VideoURL:          job.OpusClip.UploadID,
		BrandTemplateID:   profile.OpusClip.TemplateID,
		UploadedVideoAttr: &opusclip.UploadedVideoAttr{Title: job.Slug + " (full)"},
		CurationPref:      &opusclip.CurationPref{SkipCurate: true},
		RenderPref:        &opusclip.RenderPref{LayoutAspectRatio: profile.OpusClip.Full.Aspect},
	}
}
