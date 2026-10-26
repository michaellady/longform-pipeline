package cli

import (
	"os"

	"github.com/michaellady/longform-pipeline/internal/manifest"
)

func init() {
	Register(Command{
		Name:  "manifest",
		Short: "Post manifest ledger: manifest <init|ensure-clip|append-post|count-scheduled|find-clip|conflicts|schedule-ids|list-by-channel|expire|seal> --job <jobdir>",
		Run: func(args []string) int {
			if len(args) > 0 && args[0] == "seal" {
				return runSeal(args[1:])
			}
			return manifest.Main(args, os.Stdin, os.Stdout, os.Stderr)
		},
	})
}
