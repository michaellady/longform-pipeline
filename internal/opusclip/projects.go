package opusclip

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// CreateProjectRequest is the body of POST /clip-projects.
type CreateProjectRequest struct {
	VideoURL          string             `json:"videoUrl"`
	BrandTemplateID   string             `json:"brandTemplateId,omitempty"`
	UploadedVideoAttr *UploadedVideoAttr `json:"uploadedVideoAttr,omitempty"`
	CurationPref      *CurationPref      `json:"curationPref,omitempty"`
	RenderPref        *RenderPref        `json:"renderPref,omitempty"`
	ImportPreference  *ImportPreference  `json:"importPreference,omitempty"`
	ConclusionActions []ConclusionAction `json:"conclusionActions,omitempty"`
}

// UploadedVideoAttr names the project in the OpusClip dashboard.
type UploadedVideoAttr struct {
	Title string `json:"title"`
}

// CurationPref selects the curation model and clip length buckets.
type CurationPref struct {
	Model         string     `json:"model,omitempty"` // ClipBasic | ClipAnything
	ClipDurations [][2]int   `json:"clipDurations,omitempty"`
	Genre         string     `json:"genre,omitempty"`
	TopicKeywords []string   `json:"topicKeywords,omitempty"`
	CustomPrompt  string     `json:"customPrompt,omitempty"`
	Range         *ClipRange `json:"range,omitempty"`
	SkipCurate    bool       `json:"skipCurate,omitempty"`
}

// ClipRange restricts curation to a window of the source.
type ClipRange struct {
	StartSec float64 `json:"startSec,omitempty"`
	EndSec   float64 `json:"endSec,omitempty"`
}

// RenderPref selects layout and quickstart options.
type RenderPref struct {
	LayoutAspectRatio string            `json:"layoutAspectRatio,omitempty"` // portrait | landscape | square
	QuickstartConfig  *QuickstartConfig `json:"quickstartConfig,omitempty"`
}

// QuickstartConfig holds the quickstart toggles.
type QuickstartConfig struct {
	EnableRemoveFillerWords bool `json:"enableRemoveFillerWords"`
}

// ImportPreference pins the source language.
type ImportPreference struct {
	SourceLang string `json:"sourceLang,omitempty"`
}

// ConclusionAction is a webhook or email fired when the project concludes.
type ConclusionAction struct {
	Type          string `json:"type"` // WEBHOOK | EMAIL
	URL           string `json:"url,omitempty"`
	Email         string `json:"email,omitempty"`
	NotifyFailure bool   `json:"notifyFailure,omitempty"`
}

// Durations turns a list of maximum seconds into the API's [[0,N],...] shape,
// matching the vendor CLI's --durations flag.
func Durations(maxSecs []int) [][2]int {
	out := make([][2]int, 0, len(maxSecs))
	for _, d := range maxSecs {
		out = append(out, [2]int{0, d})
	}
	return out
}

// Project is the response to POST /clip-projects (live shape probed
// 2026-10-21: top-level id/projectId, stage "QUEUED", title,
// sourceInfo.durationMs, curationPref/renderPref echoed back). Raw keeps the
// verbatim payload.
type Project struct {
	ProjectID        string
	Stage            string
	Title            string
	SourceDurationMs int64
	Raw              json.RawMessage
}

// CreditEstimate is the running credit estimate for this project: OpusClip
// charges one credit per source minute (rounded up), and there is no REST
// endpoint that reports the balance, so the watcher keeps its own total.
func (p *Project) CreditEstimate() int {
	if p.SourceDurationMs <= 0 {
		return 0
	}
	return int((p.SourceDurationMs + 59_999) / 60_000)
}

// CreateProject submits a clip project (step 4 of an upload, or any URL).
func (c *Client) CreateProject(ctx context.Context, req CreateProjectRequest) (*Project, error) {
	if req.VideoURL == "" {
		return nil, fmt.Errorf("opusclip: CreateProject: videoUrl is required")
	}
	raw, err := c.post(ctx, "/clip-projects", req)
	if err != nil {
		return nil, err
	}
	p := &Project{ProjectID: stringField(raw, "projectId", "id"), Stage: stringField(raw, "stage"), Title: stringField(raw, "title"), Raw: raw}
	var env struct {
		SourceInfo struct {
			DurationMs int64 `json:"durationMs"`
		} `json:"sourceInfo"`
	}
	if json.Unmarshal(unwrapData(raw), &env) == nil {
		p.SourceDurationMs = env.SourceInfo.DurationMs
	}
	if p.ProjectID == "" {
		return p, fmt.Errorf("opusclip: clip-projects response had no projectId: %s", snippet(raw, 256))
	}
	return p, nil
}

// UpdateVisibility shares (PUBLIC) or unshares (DEFAULT) a project.
func (c *Client) UpdateVisibility(ctx context.Context, projectID, visibility string) (json.RawMessage, error) {
	return c.post(ctx, "/clip-projects/"+url.PathEscape(projectID)+"/update-visibility", map[string]string{"visibility": visibility})
}

// Templates lists brand templates (GET /brand-templates?q=mine).
func (c *Client) Templates(ctx context.Context) (json.RawMessage, error) {
	return c.get(ctx, "/brand-templates?q=mine")
}

// StorageQuota is the upload-storage usage the API reports on
// POST /upload-links (bytes used / bytes allowed; 100 GiB on the probed
// account). It is NOT a credit balance: GET /usage, /credits, /me and /user
// all answer 404 (probed 2026-10-21), so credits are only visible through the
// OAuth MCP tool or the dashboard.
type StorageQuota struct {
	UsedBytes  int64  `json:"used_bytes"`
	TotalBytes int64  `json:"total_bytes"`
	UploadID   string `json:"upload_id"` // the link this call created (unused)
}

// StorageQuota creates an upload link (harmless, no credits, the link is
// simply never used) and returns the storage counters it carries.
func (c *Client) StorageQuota(ctx context.Context) (*StorageQuota, error) {
	ul, err := c.CreateUploadLink(ctx)
	if err != nil {
		return nil, err
	}
	q := &StorageQuota{UploadID: ul.UploadID}
	if v, err := ul.UseAmount.Int64(); err == nil {
		q.UsedBytes = v
	}
	if v, err := ul.TotalAmount.Int64(); err == nil {
		q.TotalBytes = v
	}
	return q, nil
}
