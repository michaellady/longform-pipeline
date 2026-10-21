package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/michaellady/longform-pipeline/internal/model"
)

func init() {
	Register(Command{Name: "status", Short: "List jobs and their states (--json for machine output)", Run: runStatus})
}

func runStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	jobs, err := model.ListJobs()
	if err != nil {
		fmt.Fprintln(os.Stderr, "status:", err)
		return 70
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(jobs); err != nil {
			return 70
		}
		return 0
	}
	if len(jobs) == 0 {
		fmt.Println("no jobs under", model.JobsRoot())
		return 0
	}
	for _, j := range jobs {
		fmt.Printf("%-40s %-10s %-12s %s\n", j.ID, j.Profile, j.State, j.UpdatedAt.Local().Format("2006-01-02 15:04"))
	}
	return 0
}
