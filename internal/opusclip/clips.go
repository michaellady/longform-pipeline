package opusclip

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Clip is one exportable clip. Typed fields mirror the paths the vendor CLI's
// `list --summary` and `describe` modes read; Raw keeps the whole object.
//
// Live shapes (probed 2026-10-21): a skipCurate project's single clip has
// rank/score/description/hashtags null and title "<upload title>_OpusClip
// Captions"; a curated clip's hashtags is one space-separated string, not an
// array; renderAsVideoFile and renderAsVideoPreview are null until the clip
// has been re-rendered at least once; uriForExport/uriForPreview are signed
// URLs that expire and must never be treated as durable.
type Clip struct {
	ID              string          `json:"id"` // "<projectId>.<clipId>"
	CurationID      string          `json:"curationId"`
	Rank            int             `json:"rank"`
	Score           float64         `json:"score"`
	Title           string          `json:"title"`
	Description     string          `json:"description"`
	Hashtags        StringList      `json:"hashtags"`
	ClipKeywords    StringList      `json:"clipKeywords"`
	Keywords        StringList      `json:"keywords"`
	DurationMs      int64           `json:"durationMs"`
	IsBonusClip     bool            `json:"isBonusClip"`
	Text            string          `json:"text"`       // transcript (raw field)
	TranscriptField string          `json:"transcript"` // some projections use this name
	URIForPreview   string          `json:"uriForPreview"`
	URIForExport    string          `json:"uriForExport"`
	URIForThumbnail string          `json:"uriForThumbnail"`
	JudgeResult     *JudgeResult    `json:"judgeResult,omitempty"`
	RenderPref      *ClipRenderPref `json:"renderPref,omitempty"`
	RenderAsVideo   *RenderStatus   `json:"renderAsVideoFile,omitempty"`
	RenderPreview   *RenderStatus   `json:"renderAsVideoPreview,omitempty"`
	EditingScript   json.RawMessage `json:"editingScript,omitempty"`
	Raw             json.RawMessage `json:"-"`
}

// StringList decodes null, a JSON array of strings, or one whitespace
// separated string (the API returns hashtags as "#a #b #c").
type StringList []string

// UnmarshalJSON accepts null, [] of strings, or a single string.
func (s *StringList) UnmarshalJSON(b []byte) error {
	t := strings.TrimSpace(string(b))
	if t == "null" || t == "" {
		*s = nil
		return nil
	}
	if strings.HasPrefix(t, "[") {
		var arr []string
		if err := json.Unmarshal(b, &arr); err != nil {
			// tolerate arrays of non-strings by stringifying
			var anyArr []any
			if err2 := json.Unmarshal(b, &anyArr); err2 != nil {
				return err
			}
			arr = arr[:0]
			for _, v := range anyArr {
				arr = append(arr, fmt.Sprint(v))
			}
		}
		*s = arr
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	*s = strings.Fields(one)
	return nil
}

// Transcript returns the clip transcript (the raw "text" field, falling back
// to "transcript"). It is one string with literal "__silence" tokens between
// phrases and no timestamps; see CleanTranscript.
func (cl *Clip) Transcript() string {
	if cl.Text != "" {
		return cl.Text
	}
	return cl.TranscriptField
}

// CleanTranscript drops the "__silence" tokens and collapses whitespace.
func CleanTranscript(s string) string {
	words := strings.Fields(s)
	out := words[:0]
	for _, w := range words {
		if w == "__silence" {
			continue
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

// RenderReady is the completion predicate for a (re-)render: true when
// renderAsVideoFile is null (never re-rendered) or its pending flag is not
// true. Never test `.pending == false`; the object is null on fresh clips.
func (cl *Clip) RenderReady() bool { return !cl.RenderPending() }

// HasScreenOverlay reports whether the describe payload (renderPref and/or
// editingScript) carries a screen overlay: a non-empty screenOverlays array,
// a non-null screenOverlay / screenOverlayFileId, or enableScreenOverlay
// true, anywhere in the object. Absence of every such key counts as "no
// overlay" so a template with screenOverlays: [] is never re-rendered.
func (cl *Clip) HasScreenOverlay() bool {
	var v any
	if err := json.Unmarshal(cl.Raw, &v); err != nil {
		return false
	}
	return overlayIn(v)
}

func overlayIn(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			switch k {
			case "screenOverlays":
				if arr, ok := val.([]any); ok && len(arr) > 0 {
					return true
				}
			case "screenOverlay", "screenOverlayFileId":
				if val != nil {
					if s, ok := val.(string); !ok || s != "" {
						return true
					}
				}
			case "enableScreenOverlay":
				if b, ok := val.(bool); ok && b {
					return true
				}
			}
			if overlayIn(val) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if overlayIn(e) {
				return true
			}
		}
	}
	return false
}

// JudgeResult holds the per-axis virality scores.
type JudgeResult struct {
	HookScore       float64 `json:"hookScore"`
	CoherenceScore  float64 `json:"coherenceScore"`
	ConnectionScore float64 `json:"connectionScore"`
	TrendScore      float64 `json:"trendScore"`
}

// ClipRenderPref is the layout the clip was rendered with.
type ClipRenderPref struct {
	LayoutType        string `json:"layoutType"`
	LayoutAspectRatio string `json:"layoutAspectRatio"`
}

// RenderStatus is renderAsVideoFile / renderAsVideoPreview. Pending false
// means the current render (including a re-render) is complete.
type RenderStatus struct {
	Pending bool            `json:"pending"`
	Raw     json.RawMessage `json:"-"`
}

// UnmarshalJSON keeps the verbatim object alongside the typed view.
func (r *RenderStatus) UnmarshalJSON(b []byte) error {
	type plain RenderStatus
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*r = RenderStatus(p)
	r.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// ProjectID is the part of ID before the first dot.
func (cl *Clip) ProjectID() string {
	if i := strings.Index(cl.ID, "."); i >= 0 {
		return cl.ID[:i]
	}
	return ""
}

// ClipID is the part of ID after the first dot (falls back to curationId).
func (cl *Clip) ClipID() string {
	if i := strings.Index(cl.ID, "."); i >= 0 {
		return cl.ID[i+1:]
	}
	return cl.CurationID
}

// DurationSec rounds durationMs to whole seconds like the vendor CLI.
func (cl *Clip) DurationSec() int { return int((cl.DurationMs + 500) / 1000) }

// RenderPending reports whether renderAsVideoFile.pending is true.
func (cl *Clip) RenderPending() bool { return cl.RenderAsVideo != nil && cl.RenderAsVideo.Pending }

func decodeClip(b json.RawMessage) (Clip, error) {
	var cl Clip
	if err := json.Unmarshal(b, &cl); err != nil {
		return cl, err
	}
	cl.Raw = append(json.RawMessage(nil), b...)
	return cl, nil
}

// ListClips returns every exportable clip of a project
// (GET /exportable-clips?q=findByProjectId&projectId=...). An empty list
// means the project is still curating.
func (c *Client) ListClips(ctx context.Context, projectID string) ([]Clip, error) {
	raw, err := c.get(ctx, "/exportable-clips?q=findByProjectId&projectId="+url.QueryEscape(projectID))
	if err != nil {
		return nil, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(unwrapData(raw), &items); err != nil {
		return nil, fmt.Errorf("opusclip: decode clip list: %w (%s)", err, snippet(raw, 200))
	}
	out := make([]Clip, 0, len(items))
	for _, it := range items {
		cl, err := decodeClip(it)
		if err != nil {
			return nil, fmt.Errorf("opusclip: decode clip: %w", err)
		}
		out = append(out, cl)
	}
	return out, nil
}

// DescribeClip fetches one clip (GET /exportable-clips/<project>.<clip>).
// include is passed through as ?include=... (e.g. "editingScript").
func (c *Client) DescribeClip(ctx context.Context, projectID, clipID, include string) (*Clip, error) {
	p := "/exportable-clips/" + url.PathEscape(projectID+"."+clipID)
	if include != "" {
		p += "?include=" + url.QueryEscape(include)
	}
	raw, err := c.get(ctx, p)
	if err != nil {
		return nil, err
	}
	cl, err := decodeClip(unwrapData(raw))
	if err != nil {
		return nil, fmt.Errorf("opusclip: decode clip: %w", err)
	}
	return &cl, nil
}

// ReRenderRequest is the body of POST /exportable-clips/<id>/re-render.
type ReRenderRequest struct {
	EditingScript            json.RawMessage `json:"editingScript"`
	RenderPreferenceOverride map[string]any  `json:"renderPreferenceOverride,omitempty"`
}

// ReRender submits an edited script (and optional render override) for a
// server-side re-render.
func (c *Client) ReRender(ctx context.Context, projectID, clipID string, req ReRenderRequest) (json.RawMessage, error) {
	if len(req.EditingScript) == 0 || string(req.EditingScript) == "null" {
		return nil, fmt.Errorf("opusclip: ReRender: editingScript is empty")
	}
	return c.post(ctx, "/exportable-clips/"+url.PathEscape(projectID+"."+clipID)+"/re-render", req)
}

// OverlayStripOverride is the render override that removes the brand
// template's screen overlay (free re-render, verified 2026-05-18).
func OverlayStripOverride() map[string]any {
	return map[string]any{
		"screenOverlays":       []any{},
		"screenOverlay":        nil,
		"screenOverlayFileId":  nil,
		"enableScreenOverlay":  false,
		"disableScreenOverlay": true,
	}
}

// ErrNoEditingScript is returned by StripOverlay when the clip has no
// editingScript to re-render (e.g. a render that has not completed).
var ErrNoEditingScript = fmt.Errorf("opusclip: clip has no editingScript")

// ErrNoOverlay is returned by StripOverlay when the describe payload shows
// no screen overlay, so there is nothing to strip (the account's only
// template already has screenOverlays: []).
var ErrNoOverlay = fmt.Errorf("opusclip: clip has no screen overlay")

// StripOverlay fetches the clip's editingScript and, if the payload shows a
// screen overlay (or force is set), re-renders it with the overlay disabled.
// Returns the re-render response, ErrNoOverlay when skipped.
func (c *Client) StripOverlay(ctx context.Context, projectID, clipID string, force bool) (json.RawMessage, error) {
	cl, err := c.DescribeClip(ctx, projectID, clipID, "editingScript")
	if err != nil {
		return nil, err
	}
	if len(cl.EditingScript) == 0 || string(cl.EditingScript) == "null" {
		return nil, fmt.Errorf("%w: %s.%s", ErrNoEditingScript, projectID, clipID)
	}
	if !force && !cl.HasScreenOverlay() {
		return nil, fmt.Errorf("%w: %s.%s", ErrNoOverlay, projectID, clipID)
	}
	return c.ReRender(ctx, projectID, clipID, ReRenderRequest{EditingScript: cl.EditingScript, RenderPreferenceOverride: OverlayStripOverride()})
}

// WaitRender polls DescribeClip until RenderReady (renderAsVideoFile null or
// pending != true) or ctx ends. It returns the final clip.
func (c *Client) WaitRender(ctx context.Context, projectID, clipID string, interval time.Duration) (*Clip, error) {
	for {
		cl, err := c.DescribeClip(ctx, projectID, clipID, "")
		if err != nil {
			return nil, err
		}
		if cl.RenderReady() {
			return cl, nil
		}
		if err := c.sleep(ctx, interval); err != nil {
			return nil, err
		}
	}
}

// Censor submits a profanity censor job for a clip (POST /censor-jobs).
func (c *Client) Censor(ctx context.Context, projectID, clipID string, beep bool) (json.RawMessage, error) {
	return c.post(ctx, "/censor-jobs", map[string]any{"projectId": projectID, "clipId": clipID, "options": map[string]any{"beepSound": beep}})
}

// CensorStatus reads GET /censor-jobs/{jobId}.
func (c *Client) CensorStatus(ctx context.Context, jobID string) (json.RawMessage, error) {
	return c.get(ctx, "/censor-jobs/"+url.PathEscape(jobID))
}
