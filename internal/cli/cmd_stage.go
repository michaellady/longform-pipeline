package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/michaellady/longform-pipeline/internal/ingest"
	"github.com/michaellady/longform-pipeline/internal/model"
)

func init() {
	Register(Command{Name: "stage", Short: "One inbox pass for a profile: new files become staged jobs", Run: runStage})
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func runStage(args []string) int {
	fs := flag.NewFlagSet("stage", flag.ContinueOnError)
	profile := fs.String("profile", "", "profile name (required)")
	stable := fs.Int("stable-seconds", 60, "seconds a file's size and mtime must hold before it is staged")
	asJSON := fs.Bool("json", false, "emit the created jobs as JSON")
	var files multiFlag
	fs.Var(&files, "file", "stage this file instead of scanning the inbox (repeatable; the file is moved into the job)")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if *profile == "" || *profile == "all" || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: longform stage --profile <name> [--file <path>...] [--stable-seconds N] [--json]")
		return 64
	}
	p, err := model.LoadProfile(*profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "stage:", err)
		if errors.Is(err, model.ErrNoProfile) {
			return 65
		}
		return 65
	}
	logf := func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }
	jobs, err := ingest.Stage(p, ingest.Options{StableSeconds: *stable, Once: true, Files: files, Logf: logf})
	if err != nil {
		fmt.Fprintln(os.Stderr, "stage:", err)
		return 70
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if jobs == nil {
			jobs = []*model.Job{}
		}
		if err := enc.Encode(jobs); err != nil {
			return 70
		}
		return 0
	}
	if len(jobs) == 0 {
		fmt.Fprintln(os.Stderr, "stage: no new files in", p.Inbox)
		return 0
	}
	for _, j := range jobs {
		fmt.Printf("%s %s %s\n", j.State, j.ID, j.Dir())
	}
	return 0
}
