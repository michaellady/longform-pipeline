package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// State is a job's position in the pipeline. Transitions are linear; every
// command that advances a job is idempotent on re-run.
type State string

const (
	StateDetected  State = "detected"
	StateStaged    State = "staged"
	StatePrepared  State = "prepared"
	StateUploaded  State = "uploaded"
	StateClipped   State = "clipped"
	StateDrafted   State = "drafted"
	StateApproved  State = "approved"
	StateScheduled State = "scheduled"
	StateMonitored State = "monitored"
	StateClosed    State = "closed"
	StateFailed    State = "failed"
)

// Order lists the forward states in sequence.
var Order = []State{StateDetected, StateStaged, StatePrepared, StateUploaded, StateClipped, StateDrafted, StateApproved, StateScheduled, StateMonitored, StateClosed}

func (s State) index() int {
	for i, o := range Order {
		if o == s {
			return i
		}
	}
	return -1
}

// AtLeast reports whether the job has reached state s.
func (j *Job) AtLeast(s State) bool { return j.State.index() >= s.index() }

// Source is one input file (BJJ segments have several).
type Source struct {
	ID         string    `json:"id"`
	Path       string    `json:"path"` // inside the job's sources/ dir
	Original   string    `json:"original"`
	SHA256     string    `json:"sha256"`
	SizeBytes  int64     `json:"size_bytes"`
	RecordedAt time.Time `json:"recorded_at"`
	Probe      *Probe    `json:"probe,omitempty"`
}

// Probe is the subset of ffprobe output the pipeline branches on.
type Probe struct {
	DurationMs    int64  `json:"duration_ms"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	FPS           string `json:"fps"`
	Codec         string `json:"codec"`
	AudioCodec    string `json:"audio_codec"`
	ColorTransfer string `json:"color_transfer"`
	Rotation      int    `json:"rotation"`
	HDR           bool   `json:"hdr"`
}

// Transition records one state change for the audit trail.
type Transition struct {
	From State     `json:"from"`
	To   State     `json:"to"`
	At   time.Time `json:"at"`
	By   string    `json:"by"` // command name
	Note string    `json:"note,omitempty"`
}

// OpusClipRefs are the ids the upload step records before anything else.
type OpusClipRefs struct {
	ShortsProjectID string `json:"shorts_project_id,omitempty"`
	FullProjectID   string `json:"full_project_id,omitempty"`
	FullClipID      string `json:"full_clip_id,omitempty"`
	UploadID        string `json:"upload_id,omitempty"`
	OverlayStripped bool   `json:"overlay_stripped,omitempty"`
}

// Artifacts are paths relative to the job dir, filled in as stages complete.
type Artifacts struct {
	Delivery        string `json:"delivery,omitempty"`         // delivery.mp4
	Transcript      string `json:"transcript,omitempty"`       // analysis/transcript.json
	Censored        string `json:"censored,omitempty"`         // analysis/censored.json
	StabilizeBefore string `json:"stabilize_before,omitempty"` // review/stabilize-before.jpg
	StabilizeAfter  string `json:"stabilize_after,omitempty"`
	Clips           string `json:"clips,omitempty"`            // clips.json
	OpusTranscripts string `json:"opus_transcripts,omitempty"` // analysis/opus-transcripts.json
	Draft           string `json:"draft,omitempty"`            // review/draft.json (written by the skill)
	Brief           string `json:"brief,omitempty"`            // review/brief.md
	Decision        string `json:"decision,omitempty"`         // review/decision.json
	Schedule        string `json:"schedule,omitempty"`         // review/schedule.json (cells)
	Manifest        string `json:"manifest,omitempty"`         // manifest.json
	Statics         string `json:"statics,omitempty"`          // statics/
}

// Job is the per-video record the longform binary owns. Judgment never lives
// here; the skill writes its decisions into review/draft.json.
type Job struct {
	SchemaVersion string       `json:"schema_version"`
	ID            string       `json:"id"`
	Profile       string       `json:"profile"`
	Slug          string       `json:"slug"`
	State         State        `json:"state"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
	ContentHash   string       `json:"content_hash"` // sha256 over sorted source hashes
	Sources       []Source     `json:"sources"`
	Stitched      bool         `json:"stitched"`
	Delivery      *Probe       `json:"delivery_probe,omitempty"`
	OpusClip      OpusClipRefs `json:"opusclip"`
	Artifacts     Artifacts    `json:"artifacts"`
	History       []Transition `json:"history"`
	Error         string       `json:"error,omitempty"`
	Extra         map[string]any `json:"extra,omitempty"`

	dir string
}

// NewJobID builds the directory name: <YYYY-MM-DD>-<profile>-<hash8>.
func NewJobID(profile, contentHash string, now time.Time) string {
	h := contentHash
	if len(h) > 8 {
		h = h[:8]
	}
	return fmt.Sprintf("%s-%s-%s", now.Format("2006-01-02"), profile, h)
}

// JobDir returns the absolute directory for a job id.
func JobDir(id string) string { return filepath.Join(JobsRoot(), id) }

// Dir returns the job's directory.
func (j *Job) Dir() string { return j.dir }

// Path joins a job-relative path.
func (j *Job) Path(rel string) string { return filepath.Join(j.dir, rel) }

// NewJob creates the job directory and the initial job.json in StateDetected.
func NewJob(profile, contentHash string, sources []Source, now time.Time) (*Job, error) {
	id := NewJobID(profile, contentHash, now)
	dir := JobDir(id)
	if _, err := os.Stat(filepath.Join(dir, "job.json")); err == nil {
		return nil, fmt.Errorf("job %s already exists", id)
	}
	for _, sub := range []string{"sources", "analysis", "review", "statics", "logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	j := &Job{SchemaVersion: "1", ID: id, Profile: profile, Slug: id, State: StateDetected, CreatedAt: now.UTC(), UpdatedAt: now.UTC(), ContentHash: contentHash, Sources: sources, dir: dir}
	j.History = append(j.History, Transition{From: "", To: StateDetected, At: now.UTC(), By: "new"})
	return j, j.Save()
}

// LoadJob reads job.json from a job directory (absolute path or job id).
func LoadJob(dirOrID string) (*Job, error) {
	dir := dirOrID
	if !filepath.IsAbs(dir) && !strings.Contains(dir, string(os.PathSeparator)) {
		dir = JobDir(dirOrID)
	}
	dir, _ = filepath.Abs(dir)
	b, err := os.ReadFile(filepath.Join(dir, "job.json"))
	if err != nil {
		return nil, err
	}
	var j Job
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, fmt.Errorf("job.json in %s: %w", dir, err)
	}
	j.dir = dir
	return &j, nil
}

// Save writes job.json atomically (temp file + rename).
func (j *Job) Save() error {
	j.UpdatedAt = time.Now().UTC()
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(filepath.Join(j.dir, "job.json"), b, 0o644)
}

// ErrBadTransition is returned when Advance is asked to move backwards or skip.
var ErrBadTransition = errors.New("bad state transition")

// Advance moves the job to the next state. Moving to the current state is a
// no-op (idempotent re-runs); moving backwards or skipping is an error.
func (j *Job) Advance(to State, by, note string) error {
	if to == j.State {
		return nil
	}
	if to == StateFailed {
		j.History = append(j.History, Transition{From: j.State, To: to, At: time.Now().UTC(), By: by, Note: note})
		j.State = to
		j.Error = note
		return j.Save()
	}
	if to.index() != j.State.index()+1 {
		return fmt.Errorf("%w: %s -> %s", ErrBadTransition, j.State, to)
	}
	j.History = append(j.History, Transition{From: j.State, To: to, At: time.Now().UTC(), By: by, Note: note})
	j.State = to
	j.Error = ""
	return j.Save()
}

// ListJobs returns every job under JobsRoot, oldest first.
func ListJobs() ([]*Job, error) {
	entries, err := os.ReadDir(JobsRoot())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var jobs []*Job
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		j, err := LoadJob(filepath.Join(JobsRoot(), e.Name()))
		if err != nil {
			continue
		}
		jobs = append(jobs, j)
	}
	return jobs, nil
}

// WriteFileAtomic writes data to a temp file in the same directory and renames.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
