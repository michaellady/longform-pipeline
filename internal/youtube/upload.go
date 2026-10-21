package youtube

import (
	"context"
	"fmt"
	"os"

	"google.golang.org/api/googleapi"
	yt "google.golang.org/api/youtube/v3"
)

// Result is what yt-upload prints on success.
type Result struct {
	VideoID   string `json:"video_id"`
	URL       string `json:"url"`
	Privacy   string `json:"privacy"`
	PublishAt string `json:"publish_at"`
	// LockedPrivate is set when YouTube reported a privacy different from
	// the one requested, the signature of an unverified OAuth app (see
	// docs/youtube.md).
	LockedPrivate bool `json:"locked_private,omitempty"`
	// ThumbnailSet is true when --thumbnail was given and thumbnails.set
	// succeeded.
	ThumbnailSet bool `json:"thumbnail_set,omitempty"`
}

// Progress receives bytes sent so far and the total size during the
// resumable upload.
type Progress func(sent, total int64)

// Chunk size for the resumable upload; each chunk is one request and the
// library retries a failed chunk.
const uploadChunkSize = 8 << 20

// Upload performs videos.insert with a resumable media upload, then
// thumbnails.set when the request names a thumbnail. The returned result is
// non-nil whenever the video itself was created, even if the thumbnail step
// failed (the error says so), so the caller can still record the video id.
func Upload(ctx context.Context, svc *yt.Service, req *Request, progress Progress) (*Result, error) {
	video, err := BuildVideo(req)
	if err != nil {
		return nil, err
	}
	if err := CheckFiles(req); err != nil {
		return nil, err
	}

	f, err := os.Open(req.File)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	call := svc.Videos.Insert(VideoParts, video).
		Context(ctx).
		Media(f, googleapi.ChunkSize(uploadChunkSize))
	if progress != nil {
		call = call.ProgressUpdater(googleapi.ProgressUpdater(progress))
	}
	inserted, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("videos.insert: %w", err)
	}
	if inserted.Id == "" {
		return nil, fmt.Errorf("videos.insert returned no video id")
	}

	res := &Result{
		VideoID: inserted.Id,
		URL:     WatchURL(inserted.Id),
		Privacy: video.Status.PrivacyStatus,
	}
	if inserted.Status != nil {
		if inserted.Status.PrivacyStatus != "" {
			res.Privacy = inserted.Status.PrivacyStatus
		}
		res.PublishAt = inserted.Status.PublishAt
		if inserted.Status.PrivacyStatus != "" && inserted.Status.PrivacyStatus != video.Status.PrivacyStatus {
			res.LockedPrivate = true
		}
	}
	if res.PublishAt == "" {
		res.PublishAt = video.Status.PublishAt
	}

	if req.Thumbnail != "" {
		if err := SetThumbnail(ctx, svc, inserted.Id, req.Thumbnail); err != nil {
			return res, err
		}
		res.ThumbnailSet = true
	}
	return res, nil
}

// SetThumbnail uploads a custom thumbnail for an existing video.
func SetThumbnail(ctx context.Context, svc *yt.Service, videoID, path string) error {
	tf, err := os.Open(path)
	if err != nil {
		return err
	}
	defer tf.Close()
	if _, err := svc.Thumbnails.Set(videoID).Context(ctx).Media(tf).Do(); err != nil {
		return fmt.Errorf("thumbnails.set for %s: %w (the video exists; retry with `longform yt-upload` is not needed, set the thumbnail in YouTube Studio or fix the cause and re-run thumbnails.set)", videoID, err)
	}
	return nil
}

// CheckFiles verifies the video and optional thumbnail exist, are regular
// files, and the thumbnail is within YouTube's 2 MB limit. It is separate from
// Validate so --dry-run can report a metadata problem before a missing file.
func CheckFiles(req *Request) error {
	st, err := os.Stat(req.File)
	if err != nil {
		return fmt.Errorf("--file: %w", err)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("--file %s is not a regular file", req.File)
	}
	if st.Size() == 0 {
		return fmt.Errorf("--file %s is empty", req.File)
	}
	if req.Thumbnail != "" {
		ts, err := os.Stat(req.Thumbnail)
		if err != nil {
			return fmt.Errorf("--thumbnail: %w", err)
		}
		if !ts.Mode().IsRegular() {
			return fmt.Errorf("--thumbnail %s is not a regular file", req.Thumbnail)
		}
		if ts.Size() > maxThumbnailBytes {
			return fmt.Errorf("--thumbnail is %d bytes; YouTube allows %d", ts.Size(), maxThumbnailBytes)
		}
	}
	return nil
}
