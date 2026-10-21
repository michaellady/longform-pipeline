package manifest

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
)

const usage = `usage: longform manifest <sub> --job <jobdir> [flags]
  init            [--project ID] [--source-video ID] [--source-title T] [--source-url U] [--duration-seconds N] [--profile P] [--force]
  ensure-clip     --clip-id ID [--title T] [--description D] [--score N] [--duration-sec N] [--theme S] [--status post|held] [--curated-at RFC3339]
  append-post     [--file lines.jsonl]   (JSON lines {cell, response, error} from stdin by default)
  count-scheduled
  find-clip       --clip-id ID
  conflicts
  schedule-ids
  list-by-channel <label substring>
  expire          --days N [--now RFC3339]
--manifest <path> overrides <jobdir>/manifest.json.`

// Main is the `longform manifest <sub>` entry point. Exit 0 ok, 64 usage,
// 65 data error (missing clip, bad line), 70 internal.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 64
	}
	sub := args[0]
	fs := flag.NewFlagSet("manifest "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	jobDir := fs.String("job", "", "job directory")
	manifestPath := fs.String("manifest", "", "manifest path (default <job>/manifest.json)")
	// init
	project := fs.String("project", "", "scheduler project id")
	srcID := fs.String("source-video", "", "source video id")
	srcTitle := fs.String("source-title", "", "source video title")
	srcURL := fs.String("source-url", "", "source video url")
	srcDur := fs.Float64("duration-seconds", 0, "source video duration")
	profile := fs.String("profile", "", "profile name")
	force := fs.Bool("force", false, "overwrite an existing manifest")
	// ensure-clip / find-clip
	clipID := fs.String("clip-id", "", "clip id")
	title := fs.String("title", "", "clip title")
	desc := fs.String("description", "", "clip description")
	score := fs.String("score", "", "clip score")
	durSec := fs.Float64("duration-sec", 0, "clip duration")
	theme := fs.String("theme", "", "clip theme")
	status := fs.String("status", "", "post|held")
	curatedAt := fs.String("curated-at", "", "RFC3339 curation time")
	// append-post
	file := fs.String("file", "", "JSON lines file instead of stdin")
	// expire
	days := fs.Int("days", 0, "age in days after which held clips expire")
	nowFlag := fs.String("now", "", "override the clock (RFC3339)")

	// list-by-channel takes a positional needle; let flags come first or last.
	var positional []string
	rest := args[1:]
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return 64
		}
		rest = fs.Args()
		if len(rest) > 0 {
			positional = append(positional, rest[0])
			rest = rest[1:]
		}
	}

	path := *manifestPath
	if path == "" {
		if *jobDir == "" {
			fmt.Fprintln(stderr, "manifest: --job <jobdir> is required")
			fmt.Fprintln(stderr, usage)
			return 64
		}
		path = PathIn(*jobDir)
	}
	now := time.Time{}
	if *nowFlag != "" {
		t, err := time.Parse(time.RFC3339, *nowFlag)
		if err != nil {
			fmt.Fprintln(stderr, "manifest: --now:", err)
			return 64
		}
		now = t
	}

	if sub == "init" {
		wrote, err := Init(path, InitOptions{ProjectID: *project, Force: *force, Now: now, Source: SourceVideo{ID: *srcID, Title: *srcTitle, URL: *srcURL, DurationSeconds: *srcDur, Profile: *profile}})
		if err != nil {
			fmt.Fprintln(stderr, "manifest:", err)
			return 70
		}
		if wrote {
			fmt.Fprintln(stderr, "manifest: wrote", path)
		} else {
			fmt.Fprintln(stderr, "manifest: exists, left alone:", path)
		}
		return 0
	}

	m, err := Load(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && sub == "count-scheduled" {
			fmt.Fprintln(stdout, 0)
			return 0
		}
		fmt.Fprintln(stderr, "manifest:", err)
		return 65
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")

	switch sub {
	case "ensure-clip":
		if *clipID == "" {
			fmt.Fprintln(stderr, "manifest ensure-clip: --clip-id required")
			return 64
		}
		c := Clip{ClipID: *clipID, Title: *title, Description: *desc, DurationSec: *durSec, Theme: *theme, Status: *status, CuratedAt: *curatedAt}
		if *score != "" {
			f, err := strconv.ParseFloat(*score, 64)
			if err != nil {
				fmt.Fprintln(stderr, "manifest ensure-clip: --score:", err)
				return 64
			}
			c.Score = &f
		}
		if m.EnsureClip(c) {
			if err := m.Save(path); err != nil {
				fmt.Fprintln(stderr, "manifest:", err)
				return 70
			}
		}
		return 0
	case "append-post":
		in := stdin
		if *file != "" {
			f, err := os.Open(*file)
			if err != nil {
				fmt.Fprintln(stderr, "manifest append-post:", err)
				return 64
			}
			defer f.Close()
			in = f
		}
		n, err := appendLines(m, in)
		if err != nil {
			fmt.Fprintln(stderr, "manifest append-post:", err)
			return 65
		}
		if err := m.Save(path); err != nil {
			fmt.Fprintln(stderr, "manifest:", err)
			return 70
		}
		fmt.Fprintf(stderr, "manifest: appended %d posts (%d total)\n", n, m.CountScheduled())
		return 0
	case "count-scheduled":
		fmt.Fprintln(stdout, m.CountScheduled())
		return 0
	case "find-clip":
		if *clipID == "" {
			fmt.Fprintln(stderr, "manifest find-clip: --clip-id required")
			return 64
		}
		c := m.Find(*clipID)
		if c == nil {
			fmt.Fprintln(stderr, "manifest find-clip: not found:", *clipID)
			return 65
		}
		_ = enc.Encode(c)
		return 0
	case "conflicts":
		_ = enc.Encode(m.Conflicts())
		return 0
	case "schedule-ids":
		for _, id := range m.ScheduleIDs() {
			fmt.Fprintln(stdout, id)
		}
		return 0
	case "list-by-channel":
		if len(positional) == 0 {
			fmt.Fprintln(stderr, "manifest list-by-channel: label substring required")
			return 64
		}
		_ = enc.Encode(m.ListByChannel(strings.Join(positional, " ")))
		return 0
	case "expire":
		if *days <= 0 {
			fmt.Fprintln(stderr, "manifest expire: --days N (>0) required")
			return 64
		}
		fallback := curationFallback(*jobDir, m)
		expired := m.Expire(*days, fallback, now)
		if len(expired) > 0 {
			if err := m.Save(path); err != nil {
				fmt.Fprintln(stderr, "manifest:", err)
				return 70
			}
		}
		if expired == nil {
			expired = []Clip{}
		}
		_ = enc.Encode(expired)
		fmt.Fprintf(stderr, "manifest: expired %d held clips older than %d days\n", len(expired), *days)
		return 0
	default:
		fmt.Fprintf(stderr, "manifest: unknown subcommand %q\n%s\n", sub, usage)
		return 64
	}
}

// appendLines reads JSON lines ({cell, response, error}) and appends each.
func appendLines(m *Manifest, r io.Reader) (int, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	n := 0
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var l ScheduleLine
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			return n, fmt.Errorf("line %d: %w", lineNo, err)
		}
		if err := m.AppendScheduleLine(l); err != nil {
			return n, fmt.Errorf("line %d: %w", lineNo, err)
		}
		n++
	}
	return n, sc.Err()
}

// curationFallback is the clip age reference when a clip has no curated_at:
// clips.json's curation time if the job has one, else job.json created_at,
// else the manifest's created_at.
func curationFallback(jobDir string, m *Manifest) time.Time {
	if jobDir != "" {
		if raw, err := os.ReadFile(filepath.Join(jobDir, "clips.json")); err == nil {
			var head struct {
				CuratedAt string `json:"curated_at"`
				CreatedAt string `json:"created_at"`
			}
			if json.Unmarshal(raw, &head) == nil {
				for _, s := range []string{head.CuratedAt, head.CreatedAt} {
					if t, err := time.Parse(time.RFC3339, s); err == nil {
						return t
					}
				}
			}
		}
		if j, err := model.LoadJob(jobDir); err == nil && !j.CreatedAt.IsZero() {
			return j.CreatedAt
		}
	}
	if t, err := time.Parse(time.RFC3339, m.CreatedAt); err == nil {
		return t
	}
	return time.Time{}
}
