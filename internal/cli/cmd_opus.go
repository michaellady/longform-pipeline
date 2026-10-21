package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/opusclip"
)

func init() {
	Register(Command{Name: "opus", Short: "Thin OpusClip API wrappers: accounts|templates|usage|schedule|unschedule|strip-overlay|export|describe", Run: runOpus})
}

// ScheduleCell is one row of the --cells file consumed by `opus schedule`.
type ScheduleCell struct {
	ProjectID    string `json:"project_id"`
	ClipID       string `json:"clip_id"`
	Platform     string `json:"platform"`
	AccountID    string `json:"account_id"`
	SubAccountID string `json:"sub_account_id,omitempty"`
	Title        string `json:"title"`
	Description  string `json:"description,omitempty"`
	Privacy      string `json:"privacy,omitempty"`
	MediaType    string `json:"media_type,omitempty"`
	PublishAt    string `json:"publish_at"`
}

// scheduleLine is one JSON line of `opus schedule` output.
type scheduleLine struct {
	Cell       ScheduleCell    `json:"cell"`
	ScheduleID string          `json:"schedule_id,omitempty"`
	Response   json.RawMessage `json:"response,omitempty"`
	Error      string          `json:"error,omitempty"`
	Status     int             `json:"status,omitempty"`
}

func opusHelp() int {
	fmt.Fprintln(os.Stderr, `usage: longform opus <subcommand> [flags]

  accounts                                   GET /social-accounts?q=mine
  templates                                  GET /brand-templates?q=mine
  usage         [--storage]                  local credit estimate from job.json files (no REST usage endpoint exists);
                                             --storage also reads the upload-storage quota from POST /upload-links
  describe      --project P --clip C [--include editingScript]
  export        --project P --clip C [--out file]   print fresh export/preview URIs (signed, expiring), optionally download
  strip-overlay --project P --clip C [--wait] [--force]   free re-render with the screen overlay removed (skipped when none)
  schedule      --cells cells.json [--profile NAME] [--dry-run]   one JSON line per cell, 1 req/s
  unschedule    <scheduleId>                 DELETE /publish-schedules/{id}`)
	return 64
}

func runOpus(args []string) int {
	if len(args) == 0 {
		return opusHelp()
	}
	sub, rest := args[0], args[1:]
	ctx, cancel := signalContext()
	defer cancel()
	client, err := newOpusClient()
	if err != nil {
		fmt.Fprintln(os.Stderr, "opus:", err)
		return 65
	}
	switch sub {
	case "accounts":
		_, raw, err := client.Accounts(ctx)
		return emit(raw, err)
	case "templates":
		raw, err := client.Templates(ctx)
		return emit(raw, err)
	case "usage":
		return opusUsage(ctx, client, rest)
	case "describe":
		return opusDescribe(ctx, client, rest)
	case "export":
		return opusExport(ctx, client, rest)
	case "strip-overlay":
		return opusStrip(ctx, client, rest)
	case "schedule":
		return opusSchedule(ctx, client, rest)
	case "unschedule":
		if len(rest) != 1 {
			fmt.Fprintln(os.Stderr, "usage: longform opus unschedule <scheduleId>")
			return 64
		}
		raw, err := client.Unschedule(ctx, rest[0])
		return emit(raw, err)
	default:
		fmt.Fprintf(os.Stderr, "opus: unknown subcommand %q\n", sub)
		return opusHelp()
	}
}

// emit prints raw JSON (indented) or the error, mapping to exit codes.
func emit(raw json.RawMessage, err error) int {
	if err != nil {
		fmt.Fprintln(os.Stderr, "opus:", err)
		var ae *opusclip.APIError
		if errors.As(err, &ae) {
			if ae.Temporary() {
				return 75
			}
			return 65
		}
		return 70
	}
	return printJSON(raw)
}

func printJSON(v any) int {
	if raw, ok := v.(json.RawMessage); ok {
		var anyV any
		if json.Unmarshal(raw, &anyV) != nil {
			os.Stdout.Write(raw)
			os.Stdout.Write([]byte("\n"))
			return 0
		}
		v = anyV
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(os.Stderr, "opus:", err)
		return 70
	}
	return 0
}

func projectClipFlags(name string, args []string, extra func(*flag.FlagSet)) (*flag.FlagSet, string, string, bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	project := fs.String("project", "", "project id")
	clip := fs.String("clip", "", "clip id (the part after the dot)")
	if extra != nil {
		extra(fs)
	}
	if err := fs.Parse(args); err != nil {
		return fs, "", "", false
	}
	if *project == "" || *clip == "" {
		fmt.Fprintf(os.Stderr, "opus %s: --project and --clip are required\n", name)
		return fs, "", "", false
	}
	// Accept a composite "P.C" in --clip too.
	c := *clip
	if strings.HasPrefix(c, *project+".") {
		c = strings.TrimPrefix(c, *project+".")
	}
	return fs, *project, c, true
}

func opusDescribe(ctx context.Context, client *opusclip.Client, args []string) int {
	var include *string
	_, project, clip, ok := projectClipFlags("describe", args, func(fs *flag.FlagSet) {
		include = fs.String("include", "", "extra fields to include, e.g. editingScript")
	})
	if !ok {
		return 64
	}
	cl, err := client.DescribeClip(ctx, project, clip, *include)
	if err != nil {
		return emit(nil, err)
	}
	return printJSON(cl.Raw)
}

func opusExport(ctx context.Context, client *opusclip.Client, args []string) int {
	var out *string
	_, project, clip, ok := projectClipFlags("export", args, func(fs *flag.FlagSet) {
		out = fs.String("out", "", "download uriForExport to this path")
	})
	if !ok {
		return 64
	}
	cl, err := client.DescribeClip(ctx, project, clip, "")
	if err != nil {
		return emit(nil, err)
	}
	res := map[string]any{
		"project_id":     project,
		"clip_id":        clip,
		"title":          cl.Title,
		"duration_sec":   cl.DurationSec(),
		"render_pending": cl.RenderPending(),
		"uri_export":     cl.URIForExport,
		"uri_preview":    cl.URIForPreview,
	}
	if *out != "" {
		if cl.URIForExport == "" {
			fmt.Fprintln(os.Stderr, "opus export: clip has no uriForExport yet")
			return 75
		}
		n, err := download(ctx, cl.URIForExport, *out)
		if err != nil {
			fmt.Fprintln(os.Stderr, "opus export:", err)
			return 75
		}
		res["downloaded"] = *out
		res["bytes"] = n
	}
	return printJSON(res)
}

func download(ctx context.Context, u, dst string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("GET export: HTTP %d", resp.StatusCode)
	}
	tmp := dst + ".tmp-download"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return 0, err
	}
	return n, os.Rename(tmp, dst)
}

// opusUsage reports the watcher's own credit estimate. GET /usage, /credits,
// /me and /user all answer 404 (probed 2026-10-21); the only REST counter is
// the upload-storage quota on POST /upload-links, exposed behind --storage.
func opusUsage(ctx context.Context, client *opusclip.Client, args []string) int {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	storage := fs.Bool("storage", false, "also read the upload-storage quota (creates one unused upload link)")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	jobs, err := model.ListJobs()
	if err != nil {
		fmt.Fprintln(os.Stderr, "opus usage:", err)
		return 70
	}
	type jobUsage struct {
		JobID    string          `json:"job_id"`
		Profile  string          `json:"profile"`
		State    model.State     `json:"state"`
		Credits  int             `json:"credits_estimate"`
		Projects []ProjectRecord `json:"projects"`
	}
	out := map[string]any{
		"note":             "OpusClip exposes no credit balance over REST (GET /usage, /credits, /me, /user -> 404 as of 2026-10-21). credits_estimate is ceil(source minutes) per project recorded at upload time; the true balance is in the dashboard or the OAuth MCP tool opusclip_get_usage.",
		"credits_estimate": 0,
	}
	rows := []jobUsage{}
	total := 0
	for _, j := range jobs {
		recs := projectRecords(j)
		if len(recs) == 0 {
			continue
		}
		c := 0
		for _, r := range recs {
			c += r.CreditsEstimate
		}
		total += c
		rows = append(rows, jobUsage{JobID: j.ID, Profile: j.Profile, State: j.State, Credits: c, Projects: recs})
	}
	out["credits_estimate"] = total
	out["jobs"] = rows
	if *storage {
		q, err := client.StorageQuota(ctx)
		if err != nil {
			return emit(nil, err)
		}
		out["storage"] = q
	}
	return printJSON(out)
}

func opusStrip(ctx context.Context, client *opusclip.Client, args []string) int {
	var wait, force *bool
	_, project, clip, ok := projectClipFlags("strip-overlay", args, func(fs *flag.FlagSet) {
		wait = fs.Bool("wait", false, "poll until renderAsVideoFile is null or pending != true")
		force = fs.Bool("force", false, "re-render even when the describe payload shows no screen overlay")
	})
	if !ok {
		return 64
	}
	raw, err := client.StripOverlay(ctx, project, clip, *force)
	if errors.Is(err, opusclip.ErrNoOverlay) {
		return printJSON(map[string]any{"skipped": true, "reason": err.Error()})
	}
	if err != nil {
		return emit(nil, err)
	}
	if *wait {
		cl, err := client.WaitRender(ctx, project, clip, renderPollInterval)
		if err != nil {
			return emit(nil, err)
		}
		return printJSON(map[string]any{"submitted": raw, "final": cl.Raw})
	}
	return printJSON(raw)
}

func opusSchedule(ctx context.Context, client *opusclip.Client, args []string) int {
	fs := flag.NewFlagSet("schedule", flag.ContinueOnError)
	cellsPath := fs.String("cells", "", "JSON array of cells ('-' for stdin)")
	profileName := fs.String("profile", "", "resolve empty account_id from this profile's opusclip.accounts[platform]")
	dryRun := fs.Bool("dry-run", false, "print the request bodies without calling the API")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if *cellsPath == "" {
		fmt.Fprintln(os.Stderr, "opus schedule: --cells is required")
		return 64
	}
	var data []byte
	var err error
	if *cellsPath == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(*cellsPath)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "opus schedule:", err)
		return 65
	}
	data = unwrapCells(data)
	var cells []ScheduleCell
	if err := json.Unmarshal(data, &cells); err != nil {
		fmt.Fprintln(os.Stderr, "opus schedule: cells must be a JSON array:", err)
		return 65
	}
	var profile *model.Profile
	if *profileName != "" {
		if profile, err = model.LoadProfile(*profileName); err != nil {
			fmt.Fprintln(os.Stderr, "opus schedule:", err)
			return 65
		}
	}
	enc := json.NewEncoder(os.Stdout)
	failed := 0
	for _, cell := range cells {
		line := scheduleLine{Cell: cell}
		req, err := cellToRequest(cell, profile)
		switch {
		case err != nil:
			line.Error = err.Error()
		case *dryRun:
			line.Response, _ = json.Marshal(req)
		default:
			res, err := client.Schedule(ctx, req)
			if err != nil {
				line.Error = err.Error()
				var ae *opusclip.APIError
				if errors.As(err, &ae) {
					line.Status = ae.Status
				}
			} else {
				line.ScheduleID = res.ScheduleID
				line.Response = res.Raw
			}
		}
		if line.Error != "" {
			failed++
		}
		if err := enc.Encode(line); err != nil {
			return 70
		}
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "opus schedule: interrupted")
			return 75
		}
	}
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "opus schedule: %d of %d cells failed\n", failed, len(cells))
		return 65
	}
	return 0
}

// cellToRequest validates a cell and resolves its account through the
// profile when account_id is empty.
func cellToRequest(cell ScheduleCell, profile *model.Profile) (opusclip.ScheduleRequest, error) {
	acct, sub := cell.AccountID, cell.SubAccountID
	if acct == "" && profile != nil {
		acct = profile.OpusClip.Accounts[cell.Platform]
		if sub == "" {
			sub = profile.OpusClip.SubAccounts[cell.Platform]
		}
	}
	var missing []string
	if cell.ProjectID == "" {
		missing = append(missing, "project_id")
	}
	if cell.ClipID == "" {
		missing = append(missing, "clip_id")
	}
	if acct == "" {
		missing = append(missing, "account_id")
	}
	if cell.PublishAt == "" {
		missing = append(missing, "publish_at")
	}
	if len(missing) > 0 {
		return opusclip.ScheduleRequest{}, fmt.Errorf("cell missing %s", strings.Join(missing, ", "))
	}
	t, err := time.Parse(time.RFC3339, cell.PublishAt)
	if err != nil {
		return opusclip.ScheduleRequest{}, fmt.Errorf("publish_at %q is not RFC3339: %w", cell.PublishAt, err)
	}
	req := opusclip.ScheduleRequest{
		ProjectID:     cell.ProjectID,
		ClipID:        cell.ClipID,
		PostAccountID: acct,
		SubAccountID:  sub,
		PublishAt:     opusclip.FormatPublishAt(t),
		PostDetail:    opusclip.PostDetail{Title: cell.Title, MediaType: cell.MediaType},
	}
	if cell.Description != "" || cell.Privacy != "" {
		req.PostDetail.Custom = &opusclip.PostCustom{Description: cell.Description, Privacy: cell.Privacy}
	}
	return req, nil
}

// unwrapCells returns the "cells" array when data is the object that
// `brief parse-decision` writes to review/schedule.json; otherwise data as is.
func unwrapCells(data []byte) []byte {
	var wrapped struct {
		Cells json.RawMessage `json:"cells"`
	}
	if err := json.Unmarshal(data, &wrapped); err == nil && len(wrapped.Cells) > 0 && wrapped.Cells[0] == '[' {
		return wrapped.Cells
	}
	return data
}
