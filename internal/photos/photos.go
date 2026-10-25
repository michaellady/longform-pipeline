// Package photos is the second feed into the pipeline. The phone adds a
// recording to an Apple Photos album (a one-tap Share-sheet shortcut); iCloud
// Photos carries the bytes in the background; here osxphotos lists the album
// and, once an original exists on this Mac, it is cloned into a local
// incoming folder and handed to stage. Nothing is written into the iCloud
// Drive inbox, so a multi-gigabyte recording is never uploaded back to iCloud.
//
// osxphotos reads the Photos library database directly, which macOS guards
// with Full Disk Access: the watcher binary needs that grant once.
package photos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/notify"
)

// BinEnv names the osxphotos executable for the watcher (launchd has no
// login-shell PATH).
const BinEnv = "LONGFORM_OSXPHOTOS"

var (
	// ErrNoBin means osxphotos is not installed anywhere the watcher looks.
	ErrNoBin = errors.New("osxphotos not found: pipx install osxphotos (or brew install osxphotos), or set photos.osxphotos in the profile")
	// ErrNoAccess means osxphotos could not read the Photos library database.
	ErrNoAccess = errors.New("osxphotos cannot read the Photos library: grant Full Disk Access to the longform binary (System Settings → Privacy & Security → Full Disk Access) and retry")
)

// Asset is the subset of osxphotos' JSON the feed reads (field names are
// osxphotos' own, from PhotoInfo.asdict).
type Asset struct {
	UUID             string   `json:"uuid"`
	OriginalFilename string   `json:"original_filename"`
	Filename         string   `json:"filename"`
	Path             string   `json:"path"` // "" (null) until the original is on disk
	IsMissing        bool     `json:"ismissing"`
	InCloud          *bool    `json:"incloud"` // nil when not an iCloud asset
	IsCloudAsset     bool     `json:"iscloudasset"`
	IsMovie          bool     `json:"ismovie"`
	Date             string   `json:"date"`
	DateAdded        string   `json:"date_added"`
	Albums           []string `json:"albums"`
	OriginalFilesize int64    `json:"original_filesize"`
}

// Ready reports whether the original can be read right now. The database's
// ismissing flag lags a download (osxphotos documents this), so the file on
// disk is the truth.
func (a Asset) Ready() bool {
	if a.Path == "" {
		return false
	}
	st, err := os.Stat(a.Path)
	return err == nil && st.Mode().IsRegular()
}

// FindBin resolves the osxphotos executable. An explicit path (profile
// photos.osxphotos or --osxphotos) must exist: a typo there is an error naming
// it, never a silent switch to another install. Without one: $LONGFORM_OSXPHOTOS,
// PATH, then the usual install locations. Every miss wraps ErrNoBin.
func FindBin(explicit string) (string, error) {
	if explicit != "" {
		st, err := os.Stat(explicit)
		switch {
		case err != nil:
			return "", fmt.Errorf("photos.osxphotos / --osxphotos: %v: %w", err, ErrNoBin)
		case st.IsDir():
			return "", fmt.Errorf("photos.osxphotos / --osxphotos: %s is a directory: %w", explicit, ErrNoBin)
		}
		return explicit, nil
	}
	cands := []string{os.Getenv(BinEnv)}
	if p, err := exec.LookPath("osxphotos"); err == nil {
		cands = append(cands, p)
	}
	if home, err := os.UserHomeDir(); err == nil {
		cands = append(cands, filepath.Join(home, ".local", "bin", "osxphotos"))
	}
	cands = append(cands, "/opt/homebrew/bin/osxphotos", "/usr/local/bin/osxphotos")
	for _, c := range cands {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", ErrNoBin
}

// Client runs osxphotos.
type Client struct {
	Bin     string // executable (FindBin)
	Library string // optional --library override
	Logf    func(format string, args ...any)
}

func (c Client) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

func (c Client) run(ctx context.Context, args []string) (stdout, stderr []byte, err error) {
	if c.Library != "" {
		args = append(args, "--library", c.Library)
	}
	bin := c.Bin
	if strings.ContainsRune(bin, os.PathSeparator) && !filepath.IsAbs(bin) {
		// A relative path would resolve against cmd.Dir below.
		if abs, err := filepath.Abs(bin); err == nil {
			bin = abs
		}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	// osxphotos writes osxphotos_crash.log into its working directory, which
	// is "/" under launchd: keep it next to watch.log instead.
	if dir := model.LogRoot(); os.MkdirAll(dir, 0o755) == nil {
		cmd.Dir = dir
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	c.logf("$ %s %s", c.Bin, strings.Join(args, " "))
	err = cmd.Run()
	return out.Bytes(), errb.Bytes(), err
}

// diagnose turns osxphotos' failure modes into actionable errors.
func diagnose(err error, stderr []byte) error {
	s := string(stderr)
	if strings.Contains(s, "Photos.sqlite") && (strings.Contains(s, "Error copying") || strings.Contains(s, "Operation not permitted") || strings.Contains(s, "Permission denied")) {
		return fmt.Errorf("%w (details: see osxphotos_crash.log in %s)", ErrNoAccess, model.LogRoot())
	}
	msg := strings.TrimSpace(s)
	if len(msg) > 600 {
		msg = msg[:600] + "…"
	}
	if err == nil {
		return errors.New("osxphotos: " + msg)
	}
	return fmt.Errorf("osxphotos: %w: %s", err, msg)
}

// List returns the movies in album.
func (c Client) List(ctx context.Context, album string) ([]Asset, error) {
	out, stderr, err := c.run(ctx, []string{"query", "--album", album, "--only-movies", "--json", "--mute"})
	if err != nil {
		return nil, diagnose(err, stderr)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		if strings.Contains(string(stderr), "Error") {
			return nil, diagnose(nil, stderr)
		}
		return nil, nil
	}
	var assets []Asset
	if err := json.Unmarshal(out, &assets); err != nil {
		return nil, fmt.Errorf("osxphotos query: parse: %w", err)
	}
	return assets, nil
}

// Download asks Photos to fetch the original for uuid and exports it into
// dir, returning the exported file. It needs Photos access (PhotoKit) for the
// calling process, which is a second one-time grant. dir belongs to this one
// asset and is emptied first, so a retry after an interrupted export never
// returns a stale or partial file (osxphotos would write "name (1).mov").
func (c Client) Download(ctx context.Context, uuid, dir string) (string, error) {
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	_, stderr, err := c.run(ctx, []string{"export", dir, "--uuid", uuid, "--download-missing", "--use-photokit", "--skip-edited", "--no-progress"})
	if err != nil {
		return "", diagnose(err, stderr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Type().IsRegular() && !strings.HasPrefix(e.Name(), ".") {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("osxphotos export %s: nothing exported into %s", uuid, dir)
}

// Record is what the feed remembers about one album asset.
type Record struct {
	UUID             string    `json:"uuid"`
	OriginalFilename string    `json:"original_filename"`
	SizeBytes        int64     `json:"size_bytes"`
	Status           string    `json:"status"` // waiting | fed | failed
	FirstSeen        time.Time `json:"first_seen"`
	LastSeen         time.Time `json:"last_seen"`
	FedAt            time.Time `json:"fed_at,omitempty"`
	JobID            string    `json:"job_id,omitempty"`
	Incoming         string    `json:"incoming,omitempty"`
	Note             string    `json:"note,omitempty"`
	Notified         bool      `json:"notified"`
	// Attempts counts Feed calls that errored for this asset; the record
	// fails at MaxFeedAttempts.
	Attempts int `json:"attempts,omitempty"`
}

// MaxFeedAttempts is how many erroring Feed calls (stage could not run at
// all) an asset survives before its record fails for good.
const MaxFeedAttempts = 3

const (
	StatusWaiting = "waiting"
	StatusFed     = "fed"
	StatusFailed  = "failed"
)

// State is the per-profile feed memory, persisted under
// <DataRoot>/photos/<profile>.json.
type State struct {
	SchemaVersion string             `json:"schema_version"`
	Album         string             `json:"album"`
	Assets        map[string]*Record `json:"assets"`
}

// Root is where the feed keeps state and incoming clones.
func Root() string { return filepath.Join(model.DataRoot(), "photos") }

func statePath(profile string) string { return filepath.Join(Root(), profile+".json") }

// LoadState reads the profile's feed state (empty when absent).
func LoadState(profile string) (*State, error) {
	st := &State{SchemaVersion: "1", Assets: map[string]*Record{}}
	b, err := os.ReadFile(statePath(profile))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, st); err != nil {
		return nil, fmt.Errorf("%s: %w", statePath(profile), err)
	}
	if st.Assets == nil {
		st.Assets = map[string]*Record{}
	}
	return st, nil
}

// Save writes the state atomically.
func (st *State) Save(profile string) error {
	if err := os.MkdirAll(Root(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return model.WriteFileAtomic(statePath(profile), b, 0o644)
}

// FeedFile is one cloned original handed to stage, with what the feed knows
// about its Photos asset (uuid, album, original_path, original_filename,
// date) for job.extra.photos.
type FeedFile struct {
	Path  string
	Extra map[string]any
}

// FeedResult is what stage made of one FeedFile: the job it landed in; or
// neither field set, a true duplicate (its bytes are already in a job); or
// Err, why stage made nothing of it (not a readable video, the stage group
// failed, ...).
type FeedResult struct {
	JobID string
	Err   string
}

// Options tunes one pass.
type Options struct {
	// Client runs osxphotos; zero value resolves the binary from the profile,
	// the environment and PATH.
	Client Client
	// Feed stages every ready clone of the pass in one call, so a stitch
	// profile groups them by recording time exactly like an inbox batch, and
	// reports a FeedResult per path. An error means stage could not run at
	// all; the batch is retried next pass, up to MaxFeedAttempts. Required.
	Feed func(files []FeedFile) (map[string]FeedResult, error)
	// IncomingDir holds the clones handed to stage; default <Root>/incoming.
	IncomingDir string
	// Now stamps records; default time.Now.
	Now func() time.Time
	// Logf receives progress; default stderr.
	Logf func(format string, args ...any)
	// Notify delivers the one "waiting" notice per asset and the "failed"
	// notice when an asset gives up; default notify.Send.
	Notify func(p *model.Profile, event, message string)
}

func (o *Options) defaults(p *model.Profile) error {
	if o.Feed == nil {
		return errors.New("photos: Feed is required")
	}
	if o.Client.Bin == "" {
		bin, err := FindBin(p.Photos.Osxphotos)
		if err != nil {
			return err
		}
		o.Client.Bin = bin
	}
	if o.Client.Library == "" {
		o.Client.Library = p.Photos.Library
	}
	if o.IncomingDir == "" {
		o.IncomingDir = filepath.Join(Root(), "incoming")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logf == nil {
		o.Logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	}
	if o.Client.Logf == nil {
		o.Client.Logf = o.Logf
	}
	if o.Notify == nil {
		o.Notify = func(p *model.Profile, event, message string) { _ = notify.Send(p, nil, event, message) }
	}
	return nil
}

// Report counts what one pass saw.
type Report struct {
	Seen     int // movies in the album
	Waiting  int // not staged yet: original not on this Mac, or stage errored and is retried
	Deferred int // ready, held back while a stitch batch is still syncing
	Fed      int // handed to stage this pass
	Done     int // fed on an earlier pass
	Failed   int
}

// Pass lists the album once, clones every ready original into the incoming
// dir and hands them to Feed in one batch. A stitch profile holds the batch
// back while any album recording is still syncing, mirroring the inbox's
// "files still arriving" rule, so a class recorded in segments becomes one
// stitched job. Per-asset failures are recorded and logged; the error is for
// conditions that stop the pass (osxphotos missing, no library access, state
// unreadable, ctx cancelled — what the pass recorded before the cancel is
// still saved, and nothing is staged after it).
func Pass(ctx context.Context, p *model.Profile, opts Options) (Report, error) {
	var rep Report
	if !p.Photos.Enabled || p.Photos.Album == "" {
		return rep, nil
	}
	if err := opts.defaults(p); err != nil {
		return rep, err
	}
	st, err := LoadState(p.Name)
	if err != nil {
		return rep, err
	}
	st.Album = p.Photos.Album
	assets, err := opts.Client.List(ctx, p.Photos.Album)
	if err != nil {
		return rep, err
	}
	now := opts.Now()
	type readyAsset struct {
		rec      *Record
		assetDir string
		file     FeedFile
	}
	var batch []readyAsset
	var interrupted error
	for _, a := range assets {
		if err := ctx.Err(); err != nil {
			// SIGTERM from launchd: stage nothing more, keep what was recorded.
			interrupted = err
			break
		}
		if a.UUID == "" || !a.IsMovie {
			continue
		}
		rep.Seen++
		rec := st.Assets[a.UUID]
		if rec == nil {
			rec = &Record{UUID: a.UUID, OriginalFilename: a.OriginalFilename, SizeBytes: a.OriginalFilesize, Status: StatusWaiting, FirstSeen: now}
			st.Assets[a.UUID] = rec
			opts.Logf("photos %s: new in album %q: %s (%s)", p.Name, p.Photos.Album, a.OriginalFilename, HumanSize(a.OriginalFilesize))
		}
		rec.LastSeen = now
		switch rec.Status {
		case StatusFed:
			rep.Done++
			continue
		case StatusFailed:
			rep.Failed++
			continue
		}
		local := a.Path
		assetDir := filepath.Join(opts.IncomingDir, p.Name, a.UUID)
		incoming := filepath.Join(assetDir, safeName(a.OriginalFilename))
		if !a.Ready() {
			if rec.Incoming == incoming && fileExists(incoming) {
				// Complete copy from an earlier pass (a download held back
				// for a stitch batch): never download it twice.
				local = incoming
			} else if p.Photos.DownloadMissing {
				rec.Incoming = "" // an interrupted export leaves a partial file
				got, err := opts.Client.Download(ctx, a.UUID, assetDir)
				if ctx.Err() != nil {
					// The export was killed by the cancel, not a sync problem.
					interrupted = ctx.Err()
					break
				}
				if err != nil {
					rec.Note = "download: " + err.Error()
					opts.Logf("photos %s: %s: %v", p.Name, a.OriginalFilename, err)
				} else {
					local = got
				}
			}
		}
		if local == "" || !fileExists(local) {
			if !rec.Notified {
				opts.Notify(p, "waiting", fmt.Sprintf("Waiting for %s (%s) to finish syncing from iCloud Photos to this Mac", a.OriginalFilename, HumanSize(a.OriginalFilesize)))
				rec.Notified = true
			}
			rep.Waiting++
			continue
		}
		if local != incoming {
			if err := Clone(local, incoming); err != nil {
				rec.Note = "clone: " + err.Error()
				opts.Logf("photos %s: clone %s: %v", p.Name, a.OriginalFilename, err)
				rep.Waiting++
				continue
			}
		}
		rec.Incoming = incoming
		batch = append(batch, readyAsset{rec: rec, assetDir: assetDir, file: FeedFile{Path: incoming, Extra: map[string]any{
			// a.Path, not local: a downloaded export is deleted once staged.
			"uuid": a.UUID, "album": p.Photos.Album, "original_path": a.Path,
			"original_filename": a.OriginalFilename, "date": a.Date,
		}}})
	}
	if interrupted == nil {
		interrupted = ctx.Err()
	}
	switch {
	case interrupted != nil || len(batch) == 0:
	case p.Stitch.Enabled && rep.Waiting > 0:
		// The clones stay (they are cheap) and are refreshed next pass.
		rep.Deferred = len(batch)
		opts.Logf("photos %s: %d ready, %d still syncing; deferring stitch grouping until the batch is on this Mac", p.Name, len(batch), rep.Waiting)
	default:
		files := make([]FeedFile, len(batch))
		for i, b := range batch {
			files[i] = b.file
		}
		results, err := opts.Feed(files)
		if err != nil {
			opts.Logf("photos %s: stage: %v", p.Name, err)
		}
		fail := func(rec *Record, reason string) {
			rec.Status, rec.Note = StatusFailed, reason
			rep.Failed++
			opts.Logf("photos %s: %s failed: %s", p.Name, rec.OriginalFilename, reason)
			opts.Notify(p, "failed", fmt.Sprintf("Could not stage %s from the Photos album: %s", rec.OriginalFilename, reason))
		}
		for _, b := range batch {
			if err != nil {
				// Stage could not run at all (index unreadable, ...): retry
				// next pass; the clone stays.
				b.rec.Attempts++
				b.rec.Note = fmt.Sprintf("stage attempt %d/%d: %v", b.rec.Attempts, MaxFeedAttempts, err)
				if b.rec.Attempts >= MaxFeedAttempts {
					fail(b.rec, b.rec.Note)
				} else {
					rep.Waiting++
				}
				continue
			}
			res, ok := results[b.file.Path]
			if !ok {
				res.Err = "stage reported nothing for this file"
			}
			if res.Err != "" {
				// The clone is kept for inspection.
				fail(b.rec, res.Err)
				continue
			}
			b.rec.Status, b.rec.FedAt, b.rec.JobID, b.rec.Note = StatusFed, now, res.JobID, ""
			if res.JobID == "" {
				b.rec.Note = "stage created no job: duplicate of an existing job"
			}
			// stage moved the clone into the job (or left a duplicate behind);
			// what remains is ours: the leftover clone, a downloaded export and
			// osxphotos' export database.
			_ = os.RemoveAll(b.assetDir)
			rep.Fed++
			opts.Logf("photos %s: %s -> job %s", p.Name, b.rec.OriginalFilename, orDash(res.JobID))
		}
	}
	if err := st.Save(p.Name); err != nil {
		return rep, err
	}
	return rep, interrupted
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// safeName keeps the recording's own file name but never a path.
func safeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || name == "." || name == ".." || name == "/" {
		return "recording.mov"
	}
	return name
}

// HumanSize formats bytes for notifications ("3.4 GB").
func HumanSize(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}
