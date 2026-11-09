package spotify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/michaellady/longform-pipeline/internal/media"
	"github.com/michaellady/longform-pipeline/internal/model"
)

// Job-relative outputs of `longform spotify prepare`.
const (
	Dir             = "spotify"
	EpisodeFile     = "spotify/episode-1080p.mp4"
	NotesTextFile   = "spotify/episode-notes.txt"
	NotesHTMLFile   = "spotify/episode-notes.html"
	SourceCacheFile = "spotify/source-full.mp4" // downloaded OpusClip export
	DeliveryFile    = "delivery.mp4"
)

// Source kinds reported in the summary.
const (
	SourceFlag     = "flag"             // --source
	SourceCached   = "cached-export"    // a download from an earlier run
	SourceFresh    = "opusclip-export"  // fresh uriForExport from the OpusClip API
	SourceClipsURI = "clips-uri-export" // clips.json uri_export, not yet expired
	SourceDelivery = "delivery"         // <job>/delivery.mp4, the uncaptioned prepared source
)

// Encoder flag values.
const (
	EncoderAuto         = "auto"
	EncoderVideoToolbox = "videotoolbox"
	EncoderLibx264      = "libx264"
)

// ErrLimits marks a title or notes over Spotify's limits (exit 65).
var ErrLimits = errors.New("over Spotify limits")

// Options is one prepare run.
type Options struct {
	JobDir      string
	Source      string // explicit source video; "" = resolve (fresh export, clips.json, delivery.mp4)
	Title       string // full_video.title
	Description string // full_video.description (the YouTube package)
	UTMSource   string // profile spotify.notes_utm_source
	Encoder     string // auto | videotoolbox | libx264
	Force       bool
	Tools       media.Tools
	// FreshExportURL asks OpusClip for a fresh signed export URL of the full
	// clip; nil when no API client or clip id is available.
	FreshExportURL func(ctx context.Context) (string, error)
	// ClipsExportURL is clips.json's uri_export for the full clip (signed,
	// expiring); "" when unknown.
	ClipsExportURL string
	// Download fetches a URL to a local path.
	Download func(ctx context.Context, url, dst string) error
	Now      time.Time
	GOOS     string // runtime.GOOS unless set (tests)
	Log      io.Writer
}

// Result is the JSON summary `longform spotify prepare` prints.
type Result struct {
	Video          string   `json:"video"`
	NotesText      string   `json:"notes_text"`
	NotesHTML      string   `json:"notes_html"`
	Source         string   `json:"source"`
	SourceKind     string   `json:"source_kind"`
	SourceNote     string   `json:"source_note,omitempty"`
	Width          int      `json:"width"`
	Height         int      `json:"height"`
	DurationSec    float64  `json:"duration_sec"`
	VideoCodec     string   `json:"video_codec"`
	AudioCodec     string   `json:"audio_codec"`
	Encoder        string   `json:"encoder,omitempty"` // ffmpeg encoder used; "" when copied or skipped
	Reencoded      bool     `json:"reencoded"`
	VideoSkipped   bool     `json:"video_skipped"` // output was newer than the source
	NotesSkipped   bool     `json:"notes_skipped"` // notes were already up to date
	Title          string   `json:"title"`
	TitleChars     int      `json:"title_chars"`
	NotesChars     int      `json:"notes_chars"`
	NotesHTMLChars int      `json:"notes_html_chars"`
	Problems       []string `json:"problems"`
}

// Prepare writes the episode video and notes into <job>/spotify/.
// Idempotent: the video is re-made only when missing, older than its
// source, or with Force; the notes are rewritten only when their content
// changes.
func Prepare(ctx context.Context, o Options) (*Result, error) {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if err := os.MkdirAll(filepath.Join(o.JobDir, Dir), 0o755); err != nil {
		return nil, err
	}
	res := &Result{
		Video:     filepath.Join(o.JobDir, EpisodeFile),
		NotesText: filepath.Join(o.JobDir, NotesTextFile),
		NotesHTML: filepath.Join(o.JobDir, NotesHTMLFile),
		Title:     o.Title,
		Problems:  []string{},
	}

	// Notes first: cheap, and the limits check should fail before a long encode.
	notes := Notes(o.Description, o.UTMSource)
	notesHTML := NotesHTML(notes)
	res.TitleChars, res.NotesChars, res.NotesHTMLChars = RuneLen(o.Title), RuneLen(notes), RuneLen(notesHTML)
	res.Problems = append(res.Problems, CheckLimits(o.Title, notes)...)
	w1, err := writeIfChanged(res.NotesText, notes+"\n")
	if err != nil {
		return res, err
	}
	w2, err := writeIfChanged(res.NotesHTML, notesHTML+"\n")
	if err != nil {
		return res, err
	}
	res.NotesSkipped = !w1 && !w2

	src, kind, note, err := resolveSource(ctx, o)
	if err != nil {
		return res, err
	}
	res.Source, res.SourceKind, res.SourceNote = src, kind, note

	if !o.Force && newerThan(res.Video, src) {
		res.VideoSkipped = true
	} else {
		info, err := o.Tools.Probe(src)
		if err != nil {
			return res, fmt.Errorf("probe source %s: %w", src, err)
		}
		encoder := ""
		if needsEncode(info) {
			enc, err := resolveEncoder(o)
			if err != nil {
				return res, err
			}
			encoder = enc
		}
		partial := res.Video + ".partial.mp4"
		args, reencode := EncodeArgs(src, partial, info, encoder)
		if err := o.Tools.RunFFmpeg(o.JobDir, args...); err != nil {
			os.Remove(partial)
			return res, err
		}
		if err := os.Rename(partial, res.Video); err != nil {
			return res, err
		}
		res.Reencoded = reencode
		if reencode {
			res.Encoder = encoder
		}
	}

	out, err := o.Tools.Probe(res.Video)
	if err != nil {
		return res, fmt.Errorf("probe episode %s: %w", res.Video, err)
	}
	res.Width, res.Height = DisplaySize(out)
	res.DurationSec = float64(out.DurationMs) / 1000
	res.VideoCodec, res.AudioCodec = out.VideoCodec, out.AudioCodec
	if !Is16x9(res.Width, res.Height) {
		res.Problems = append(res.Problems, fmt.Sprintf("episode is %dx%d, not 16:9", res.Width, res.Height))
	}
	if out.VideoCodec != "h264" {
		res.Problems = append(res.Problems, "episode video is "+out.VideoCodec+", not h264")
	}
	if !out.HasAudio {
		res.Problems = append(res.Problems, "episode has no audio stream")
	}
	if len(CheckLimits(o.Title, notes)) > 0 {
		return res, ErrLimits
	}
	return res, nil
}

func needsEncode(info *media.Info) bool {
	w, h := DisplaySize(info)
	return !(Is16x9(w, h) && info.VideoCodec == "h264")
}

// resolveEncoder maps the --encoder flag onto media.ResolveEncoder (auto:
// VideoToolbox on a Mac whose ffmpeg passes its test encode, else libx264).
func resolveEncoder(o Options) (string, error) {
	setting := model.PrepareEncoderAuto
	switch o.Encoder {
	case "", EncoderAuto:
	case EncoderVideoToolbox:
		setting = model.PrepareEncoderVideoToolbox
	case EncoderLibx264, "x264":
		setting = model.PrepareEncoderX264
	default:
		return "", fmt.Errorf("--encoder must be auto, videotoolbox or libx264, got %q", o.Encoder)
	}
	enc, err := media.ResolveEncoder(setting, o.GOOS, o.Tools.VideoToolboxCaps)
	if err != nil {
		return "", err
	}
	return enc.EncoderName(), nil
}

// resolveSource picks the video to convert: --source; else an export
// downloaded by an earlier run; else a fresh OpusClip export; else
// clips.json's uri_export while it has not expired; else delivery.mp4.
func resolveSource(ctx context.Context, o Options) (path, kind, note string, err error) {
	if o.Source != "" {
		if _, err := os.Stat(o.Source); err != nil {
			return "", "", "", fmt.Errorf("--source: %w", err)
		}
		return o.Source, SourceFlag, "", nil
	}
	cache := filepath.Join(o.JobDir, SourceCacheFile)
	if !o.Force {
		if st, err := os.Stat(cache); err == nil && st.Size() > 0 {
			return cache, SourceCached, "", nil
		}
	}
	var notes []string
	if o.FreshExportURL != nil && o.Download != nil {
		u, err := o.FreshExportURL(ctx)
		if err == nil && u != "" {
			if err = o.Download(ctx, u, cache); err == nil {
				return cache, SourceFresh, "", nil
			}
		}
		if err == nil {
			err = errors.New("no uriForExport yet")
		}
		notes = append(notes, "fresh OpusClip export unavailable: "+err.Error())
	} else {
		notes = append(notes, "no OpusClip client or full clip id: fresh export not tried")
	}
	if o.ClipsExportURL != "" && o.Download != nil {
		if exp, known := URLExpired(o.ClipsExportURL, o.Now); exp && known {
			notes = append(notes, "clips.json uri_export has expired")
		} else if err := o.Download(ctx, o.ClipsExportURL, cache); err == nil {
			return cache, SourceClipsURI, joinNotes(notes), nil
		} else {
			notes = append(notes, "clips.json uri_export download failed: "+err.Error())
		}
	}
	delivery := filepath.Join(o.JobDir, DeliveryFile)
	if _, err := os.Stat(delivery); err == nil {
		notes = append(notes, "using delivery.mp4, the uncaptioned prepared source; pass --source for a captioned export")
		return delivery, SourceDelivery, joinNotes(notes), nil
	}
	return "", "", "", fmt.Errorf("no source video: %s; pass --source", joinNotes(append(notes, "no delivery.mp4")))
}

func joinNotes(n []string) string { return strings.Join(n, "; ") }

// URLExpired reads the expiry of a signed URL: Expires=<unix seconds>
// (GCS V2, CloudFront), or X-Goog-Date/X-Amz-Date plus X-Goog-Expires/
// X-Amz-Expires seconds (V4). known is false when the URL carries neither.
func URLExpired(raw string, now time.Time) (expired, known bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return false, false
	}
	q := u.Query()
	if v := q.Get("Expires"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return !now.Before(time.Unix(n, 0)), true
		}
	}
	for _, p := range []string{"X-Goog-", "X-Amz-"} {
		d, e := q.Get(p+"Date"), q.Get(p+"Expires")
		if d == "" || e == "" {
			continue
		}
		t, err1 := time.Parse("20060102T150405Z", d)
		secs, err2 := strconv.ParseInt(e, 10, 64)
		if err1 == nil && err2 == nil {
			return !now.Before(t.Add(time.Duration(secs) * time.Second)), true
		}
	}
	return false, false
}

// newerThan reports whether out exists, is non-empty and is at least as new
// as in.
func newerThan(out, in string) bool {
	so, err := os.Stat(out)
	if err != nil || so.Size() == 0 {
		return false
	}
	si, err := os.Stat(in)
	if err != nil {
		return false
	}
	return !so.ModTime().Before(si.ModTime())
}

// writeIfChanged writes content atomically unless the file already holds
// it. Reports whether it wrote.
func writeIfChanged(path, content string) (bool, error) {
	if b, err := os.ReadFile(path); err == nil && string(b) == content {
		return false, nil
	}
	return true, model.WriteFileAtomic(path, []byte(content), 0o644)
}
