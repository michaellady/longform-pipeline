package youtube

import (
	"fmt"
	"strings"
	"time"

	yt "google.golang.org/api/youtube/v3"
)

// YouTube Data API limits (developers.google.com/youtube/v3/docs/videos).
const (
	maxTitleLen       = 100  // characters
	maxDescriptionLen = 5000 // bytes
	maxTagsLen        = 500  // characters, all tags combined
	maxThumbnailBytes = 2 << 20
)

// Privacy values accepted by status.privacyStatus.
const (
	PrivacyPublic   = "public"
	PrivacyUnlisted = "unlisted"
	PrivacyPrivate  = "private"
)

// Request is everything needed for one upload. Privacy "" means private.
type Request struct {
	File        string
	Title       string
	Description string
	Chapters    []Chapter
	Thumbnail   string
	Privacy     string
	PublishAt   time.Time // zero means publish immediately at Privacy
	Tags        []string
	CategoryID  string
	MadeForKids bool
}

// EffectivePrivacy is the privacy that will be sent: private when scheduling,
// private when unset, otherwise what was asked for.
func (r *Request) EffectivePrivacy() string {
	if !r.PublishAt.IsZero() || r.Privacy == "" {
		return PrivacyPrivate
	}
	return r.Privacy
}

// Validate checks every field that does not need the network.
func (r *Request) Validate() error { return r.validateAt(time.Now()) }

func (r *Request) validateAt(now time.Time) error {
	if strings.TrimSpace(r.File) == "" {
		return fmt.Errorf("--file is required")
	}
	title := strings.TrimSpace(r.Title)
	if title == "" {
		return fmt.Errorf("--title is required")
	}
	if n := len([]rune(title)); n > maxTitleLen {
		return fmt.Errorf("title is %d characters; YouTube allows %d", n, maxTitleLen)
	}
	if strings.ContainsAny(title, "<>") {
		return fmt.Errorf("title must not contain < or >")
	}
	desc := AppendChapters(r.Description, r.Chapters)
	if len(desc) > maxDescriptionLen {
		return fmt.Errorf("description with chapters is %d bytes; YouTube allows %d", len(desc), maxDescriptionLen)
	}
	if strings.ContainsAny(desc, "<>") {
		return fmt.Errorf("description must not contain < or >")
	}
	if len(r.Chapters) > 0 {
		if err := ValidateChapters(r.Chapters); err != nil {
			return err
		}
	}
	switch r.Privacy {
	case "", PrivacyPublic, PrivacyUnlisted, PrivacyPrivate:
	default:
		return fmt.Errorf("--privacy must be public, unlisted or private, got %q", r.Privacy)
	}
	if !r.PublishAt.IsZero() {
		if r.Privacy != "" && r.Privacy != PrivacyPrivate {
			return fmt.Errorf("--publish-at requires --privacy private (the video is private until YouTube publishes it); got %q", r.Privacy)
		}
		if !r.PublishAt.After(now) {
			return fmt.Errorf("--publish-at %s is not in the future", r.PublishAt.Format(time.RFC3339))
		}
	}
	total := 0
	for _, t := range r.Tags {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("tags must not be empty")
		}
		if strings.ContainsAny(t, "<>") {
			return fmt.Errorf("tag %q must not contain < or >", t)
		}
		total += len([]rune(t))
	}
	if total > maxTagsLen {
		return fmt.Errorf("tags total %d characters; YouTube allows %d", total, maxTagsLen)
	}
	if r.CategoryID != "" {
		for _, c := range r.CategoryID {
			if c < '0' || c > '9' {
				return fmt.Errorf("--category must be a numeric YouTube category id, got %q", r.CategoryID)
			}
		}
	}
	if r.Thumbnail != "" {
		ext := strings.ToLower(r.Thumbnail)
		if !strings.HasSuffix(ext, ".jpg") && !strings.HasSuffix(ext, ".jpeg") && !strings.HasSuffix(ext, ".png") {
			return fmt.Errorf("--thumbnail must be a .jpg, .jpeg or .png file")
		}
	}
	return nil
}

// BuildVideo turns the request into the videos.insert body (snippet + status).
// It does not touch the filesystem or the network.
func BuildVideo(r *Request) (*yt.Video, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	v := &yt.Video{
		Snippet: &yt.VideoSnippet{
			Title:       strings.TrimSpace(r.Title),
			Description: AppendChapters(r.Description, r.Chapters),
			CategoryId:  r.CategoryID,
		},
		Status: &yt.VideoStatus{
			PrivacyStatus:           r.EffectivePrivacy(),
			SelfDeclaredMadeForKids: r.MadeForKids,
			// YouTube requires an explicit made-for-kids declaration; a false
			// bool is omitted by the JSON encoder unless forced.
			ForceSendFields: []string{"SelfDeclaredMadeForKids"},
		},
	}
	if len(r.Tags) > 0 {
		tags := make([]string, 0, len(r.Tags))
		for _, t := range r.Tags {
			tags = append(tags, strings.TrimSpace(t))
		}
		v.Snippet.Tags = tags
	}
	if !r.PublishAt.IsZero() {
		v.Status.PublishAt = r.PublishAt.UTC().Format(time.RFC3339)
	}
	return v, nil
}

// VideoParts is the part list for videos.insert matching BuildVideo.
var VideoParts = []string{"snippet", "status"}

// WatchURL is the public URL of a video id.
func WatchURL(id string) string { return "https://www.youtube.com/watch?v=" + id }
