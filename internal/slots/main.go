package slots

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/michaellady/longform-pipeline/internal/gates"
	"github.com/michaellady/longform-pipeline/internal/model"
)

// Main is the `longform slots <sub>` entry point. The filled cells array goes
// to stdout, the summary to stderr. Exit 0 ok, 64 usage, 70 internal.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "propose" {
		fmt.Fprintln(stderr, "usage: longform slots propose --profile <name> --cells <file> [--existing <file>] [--from YYYY-MM-DD] [--approved-at RFC3339] [--now RFC3339]")
		return 64
	}
	fs := flag.NewFlagSet("slots propose", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profile := fs.String("profile", "", "profile name under "+model.ProfilesDir())
	cellsPath := fs.String("cells", "", "cells JSON array; - for stdin")
	existingPath := fs.String("existing", "", "already-scheduled cells (live OpusClip schedule + Buffer queue)")
	from := fs.String("from", "", "earliest local day for the full video (YYYY-MM-DD)")
	approvedAt := fs.String("approved-at", "", "approval time (RFC3339); default now")
	nowFlag := fs.String("now", "", "override the clock (RFC3339), for reproducible runs")
	if err := fs.Parse(args[1:]); err != nil {
		return 64
	}
	if *profile == "" || *cellsPath == "" {
		fmt.Fprintln(stderr, "slots: --profile and --cells are required")
		return 64
	}
	p, err := model.LoadProfile(*profile)
	if err != nil {
		fmt.Fprintln(stderr, "slots:", err)
		return 64
	}
	var cells []gates.Cell
	if *cellsPath == "-" {
		cells, err = gates.ReadCells(stdin)
	} else {
		cells, err = gates.LoadCells(*cellsPath)
	}
	if err != nil {
		fmt.Fprintln(stderr, "slots:", err)
		return 64
	}
	existing, err := gates.LoadCells(*existingPath)
	if err != nil {
		fmt.Fprintln(stderr, "slots:", err)
		return 64
	}
	opt := Options{From: *from}
	if *approvedAt != "" {
		if opt.ApprovedAt, err = time.Parse(time.RFC3339, *approvedAt); err != nil {
			fmt.Fprintln(stderr, "slots: --approved-at:", err)
			return 64
		}
	}
	if *nowFlag != "" {
		if opt.Now, err = time.Parse(time.RFC3339, *nowFlag); err != nil {
			fmt.Fprintln(stderr, "slots: --now:", err)
			return 64
		}
	}
	out, sum, err := Propose(p, cells, existing, opt)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 70
	}
	if err := gates.WriteCells(stdout, out); err != nil {
		fmt.Fprintln(stderr, "slots:", err)
		return 70
	}
	fmt.Fprintln(stderr, sum.String())
	return 0
}
