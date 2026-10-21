package youtube

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

// Options are the flags that shape the run rather than the video.
type Options struct {
	DryRun bool
}

// UsageError marks a problem with the flags themselves (exit 64) as opposed
// to a problem with the files they name (exit 65).
type UsageError struct{ Err error }

func (e *UsageError) Error() string { return e.Err.Error() }
func (e *UsageError) Unwrap() error { return e.Err }

func usage(err error) error { return &UsageError{Err: err} }

// ParseArgs parses the yt-upload flags into a validated Request. Flag errors
// and invalid flag values come back as *UsageError; a chapters file that
// cannot be read or fails YouTube's rules comes back as a plain error. On
// -h/--help the returned error wraps flag.ErrHelp.
func ParseArgs(args []string, stderr io.Writer) (*Request, *Options, error) {
	fs := flag.NewFlagSet("yt-upload", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: longform yt-upload --file <mp4> --title <t> [--description <d>] [flags]")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Uploads a finished video to YouTube with metadata, chapters, thumbnail,")
		fmt.Fprintln(stderr, "privacy and an optional scheduled publish time. See docs/youtube.md.")
		fmt.Fprintln(stderr)
		fs.PrintDefaults()
	}

	var (
		req  Request
		opts Options

		chaptersFile string
		publishAt    string
		tags         string
	)
	fs.StringVar(&req.File, "file", "", "video file to upload (required)")
	fs.StringVar(&req.Title, "title", "", "video title, at most 100 characters (required)")
	fs.StringVar(&req.Description, "description", "", "video description; chapters are appended when --chapters-file is given")
	fs.StringVar(&chaptersFile, "chapters-file", "", `JSON array of {"at_ms":0,"title":"Intro"}; first at 0, at least three, each 10 s or longer`)
	fs.StringVar(&req.Thumbnail, "thumbnail", "", "custom thumbnail (.jpg or .png, up to 2 MB); needs a phone-verified channel")
	fs.StringVar(&req.Privacy, "privacy", "", "public, unlisted or private (default private; forced private with --publish-at)")
	fs.StringVar(&publishAt, "publish-at", "", "RFC3339 time to go public, e.g. 2026-10-22T16:00:00Z; the video is private until then")
	fs.StringVar(&tags, "tags", "", "comma-separated tags, 500 characters total at most")
	fs.StringVar(&req.CategoryID, "category", "", "numeric YouTube category id, e.g. 22 (People & Blogs) or 28 (Science & Technology)")
	fs.BoolVar(&req.MadeForKids, "made-for-kids", false, "declare the video as made for kids (COPPA)")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "print the videos.insert body and exit without any network access")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, nil, err
		}
		return nil, nil, usage(err)
	}
	if fs.NArg() > 0 {
		return nil, nil, usage(fmt.Errorf("unexpected positional arguments: %v", fs.Args()))
	}

	if publishAt != "" {
		t, err := time.Parse(time.RFC3339, publishAt)
		if err != nil {
			return nil, nil, usage(fmt.Errorf("--publish-at must be RFC3339 (2026-10-22T16:00:00Z or 2026-10-22T09:00:00-07:00): %w", err))
		}
		req.PublishAt = t
	}
	req.Tags = SplitTags(tags)

	if chaptersFile != "" {
		ch, err := ParseChaptersFile(chaptersFile)
		if err != nil {
			return nil, nil, err
		}
		req.Chapters = ch
	}

	if err := req.Validate(); err != nil {
		return nil, nil, usage(err)
	}
	return &req, &opts, nil
}

// SplitTags splits a comma-separated list, trimming whitespace and dropping
// empty entries.
func SplitTags(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}
