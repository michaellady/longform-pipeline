package opusclip

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// SocialAccount is one connected channel (GET /social-accounts?q=mine).
type SocialAccount struct {
	PostAccountID      string          `json:"postAccountId"`
	SubAccountID       string          `json:"subAccountId"`
	Platform           string          `json:"platform"` // YOUTUBE | TIKTOK_BUSINESS | FACEBOOK_PAGE | INSTAGRAM_BUSINESS | LINKEDIN | TWITTER
	ExtUserID          string          `json:"extUserId"`
	ExtUserName        string          `json:"extUserName"`
	ExtUserPictureLink string          `json:"extUserPictureLink"`
	ExtUserProfileLink string          `json:"extUserProfileLink"`
	Raw                json.RawMessage `json:"-"`
}

// Accounts lists connected social accounts.
func (c *Client) Accounts(ctx context.Context) ([]SocialAccount, json.RawMessage, error) {
	raw, err := c.get(ctx, "/social-accounts?q=mine")
	if err != nil {
		return nil, nil, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(unwrapData(raw), &items); err != nil {
		return nil, raw, fmt.Errorf("opusclip: decode social accounts: %w (%s)", err, snippet(raw, 200))
	}
	out := make([]SocialAccount, 0, len(items))
	for _, it := range items {
		var a SocialAccount
		if err := json.Unmarshal(it, &a); err != nil {
			return nil, raw, err
		}
		a.Raw = it
		out = append(out, a)
	}
	return out, raw, nil
}

// PostDetail is the metadata of a scheduled or immediate post.
type PostDetail struct {
	Title     string      `json:"title"`
	MediaType string      `json:"mediaType,omitempty"`
	Custom    *PostCustom `json:"custom,omitempty"`
}

// PostCustom holds the platform-specific extras.
type PostCustom struct {
	Description string `json:"description,omitempty"`
	Privacy     string `json:"privacy,omitempty"` // public | private | unlisted
}

// ScheduleRequest is the body of POST /publish-schedules.
type ScheduleRequest struct {
	ProjectID     string     `json:"projectId"`
	ClipID        string     `json:"clipId"`
	PostAccountID string     `json:"postAccountId"`
	SubAccountID  string     `json:"subAccountId,omitempty"`
	PublishAt     string     `json:"publishAt"` // ISO 8601 UTC
	PostDetail    PostDetail `json:"postDetail"`
}

// PostRequest is the body of POST /post-tasks (publish now).
type PostRequest struct {
	ProjectID     string     `json:"projectId"`
	ClipID        string     `json:"clipId"`
	PostAccountID string     `json:"postAccountId"`
	SubAccountID  string     `json:"subAccountId,omitempty"`
	PostDetail    PostDetail `json:"postDetail"`
}

// ScheduleResult is the parsed answer to a schedule call.
type ScheduleResult struct {
	ScheduleID string
	Raw        json.RawMessage
}

// FormatPublishAt renders t as the ISO 8601 UTC string the API expects.
func FormatPublishAt(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// Schedule creates a publish schedule (paced at 1 req/s).
func (c *Client) Schedule(ctx context.Context, req ScheduleRequest) (*ScheduleResult, error) {
	switch {
	case req.ProjectID == "", req.ClipID == "", req.PostAccountID == "", req.PublishAt == "":
		return nil, fmt.Errorf("opusclip: Schedule: projectId, clipId, postAccountId and publishAt are required")
	}
	raw, err := c.postPaced(ctx, "/publish-schedules", req)
	if err != nil {
		return nil, err
	}
	return &ScheduleResult{ScheduleID: stringField(raw, "scheduleId", "id"), Raw: raw}, nil
}

// Unschedule cancels a publish schedule (paced at 1 req/s).
func (c *Client) Unschedule(ctx context.Context, scheduleID string) (json.RawMessage, error) {
	return c.delPaced(ctx, "/publish-schedules/"+url.PathEscape(scheduleID))
}

// PostNow publishes immediately (POST /post-tasks, paced at 1 req/s).
func (c *Client) PostNow(ctx context.Context, req PostRequest) (json.RawMessage, error) {
	return c.postPaced(ctx, "/post-tasks", req)
}

// SocialCopyRequest is the body of POST /social-copy-jobs.
type SocialCopyRequest struct {
	ProjectID       string `json:"projectId"`
	ClipID          string `json:"clipId"`
	PostAccountID   string `json:"postAccountId"`
	SubAccountID    string `json:"subAccountId,omitempty"`
	Prompt          string `json:"prompt,omitempty"`
	ForceRegenerate bool   `json:"forceRegenerate,omitempty"`
}

// SocialCopy starts a platform-copy generation job (paced at 1 req/s).
func (c *Client) SocialCopy(ctx context.Context, req SocialCopyRequest) (json.RawMessage, error) {
	return c.postPaced(ctx, "/social-copy-jobs", req)
}

// SocialCopyResult polls GET /social-copy-jobs/{jobId}.
func (c *Client) SocialCopyResult(ctx context.Context, jobID string) (json.RawMessage, error) {
	return c.get(ctx, "/social-copy-jobs/"+url.PathEscape(jobID))
}

// PendingSchedule is one entry of GET /publish-schedules?q=findByProjectAndClip.
// The endpoint is undocumented; the API names it (with q=byPage) in its 400
// for an unknown q (probed 2026-10-25, docs/probes/2026-10-25-publish-schedules-read.json).
// It lists only schedules still pending: once a post goes out the schedule
// leaves the list, and the response carries no status, post URL or failure
// reason, so "absent" never tells posted from failed or cancelled.
type PendingSchedule struct {
	ScheduleID string          `json:"scheduleId"`
	Platform   string          `json:"platform"`
	PublishAt  string          `json:"publishAt"`
	ExtUserID  string          `json:"extUserId"`
	ExtOrgID   string          `json:"extOrgId"`
	Raw        json.RawMessage `json:"-"`
}

// ClipSchedules lists the pending publish schedules of one clip (clipID ""
// lists the whole project). Each entry keeps its verbatim JSON in Raw.
func (c *Client) ClipSchedules(ctx context.Context, projectID, clipID string) ([]PendingSchedule, error) {
	if projectID == "" {
		return nil, fmt.Errorf("opusclip: ClipSchedules: projectId is required")
	}
	q := url.Values{"q": {"findByProjectAndClip"}, "projectId": {projectID}}
	if clipID != "" {
		q.Set("clipId", clipID)
	}
	raw, err := c.get(ctx, "/publish-schedules?"+q.Encode())
	if err != nil {
		return nil, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(unwrapData(raw), &items); err != nil {
		return nil, fmt.Errorf("opusclip: decode publish schedules: %w (%s)", err, snippet(raw, 200))
	}
	out := make([]PendingSchedule, 0, len(items))
	for _, it := range items {
		var s PendingSchedule
		if err := json.Unmarshal(it, &s); err != nil {
			return nil, fmt.Errorf("opusclip: decode publish schedule: %w (%s)", err, snippet(it, 200))
		}
		s.Raw = it
		out = append(out, s)
	}
	return out, nil
}
