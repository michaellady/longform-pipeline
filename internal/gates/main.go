package gates

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
)

// Main is the `longform gate <sub>` entry point. Exit codes follow sysexits:
// 0 every cell passes, 64 usage, 65 at least one verdict, 70 internal.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "validate" {
		fmt.Fprintln(stderr, "usage: longform gate validate --profile <name> --cells <file> [--existing <file>] [--censor-list <path>] [--now RFC3339]")
		return 64
	}
	fs := flag.NewFlagSet("gate validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profile := fs.String("profile", "", "profile name under "+model.ProfilesDir())
	cellsPath := fs.String("cells", "", "cells JSON array (docs/cells-schema.md); - for stdin")
	existingPath := fs.String("existing", "", "already-scheduled cells, counted toward cadence")
	censorPath := fs.String("censor-list", "", "profanity list; default censor/default.json next to the plugin binary")
	nowFlag := fs.String("now", "", "override the clock (RFC3339), for reproducible runs")
	if err := fs.Parse(args[1:]); err != nil {
		return 64
	}
	if *profile == "" || *cellsPath == "" {
		fmt.Fprintln(stderr, "gate: --profile and --cells are required")
		return 64
	}
	p, err := model.LoadProfile(*profile)
	if err != nil {
		fmt.Fprintln(stderr, "gate:", err)
		return 64
	}
	var cells []Cell
	if *cellsPath == "-" {
		cells, err = ReadCells(stdin)
	} else {
		cells, err = LoadCells(*cellsPath)
	}
	if err != nil {
		fmt.Fprintln(stderr, "gate:", err)
		return 64
	}
	existing, err := LoadCells(*existingPath)
	if err != nil {
		fmt.Fprintln(stderr, "gate:", err)
		return 64
	}
	opt := Options{}
	if *nowFlag != "" {
		opt.Now, err = time.Parse(time.RFC3339, *nowFlag)
		if err != nil {
			fmt.Fprintln(stderr, "gate: --now:", err)
			return 64
		}
	}
	var extra []string
	if p.Censor.Enabled {
		extra = p.Censor.Extra
	}
	cz, path, cerr := ResolveCensor(*censorPath, extra)
	if cerr != nil {
		if !errors.Is(cerr, ErrNoCensorList) || *censorPath != "" {
			fmt.Fprintln(stderr, "gate:", cerr)
			return 64
		}
		fmt.Fprintf(stderr, "gate: WARN %v; profanity check uses profile censor.extra only (%d words)\n", cerr, cz.Len())
	} else {
		fmt.Fprintf(stderr, "gate: censor list %s (%d words)\n", path, cz.Len())
	}
	opt.Censor = cz

	verdicts, err := Validate(p, cells, existing, opt)
	if err != nil {
		fmt.Fprintln(stderr, "gate:", err)
		return 70
	}
	if len(verdicts) == 0 {
		fmt.Fprintf(stderr, "gate: %d cells pass\n", len(cells))
		return 0
	}
	enc := json.NewEncoder(stdout)
	for _, v := range verdicts {
		_ = enc.Encode(v) // one JSON object per line
	}
	fmt.Fprintf(stderr, "gate: %d verdicts over %d cells\n", len(verdicts), len(cells))
	return 65
}
