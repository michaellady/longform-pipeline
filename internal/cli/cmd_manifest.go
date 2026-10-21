package cli

import (
	"os"

	"github.com/michaellady/longform-pipeline/internal/manifest"
)

func init() {
	Register(Command{
		Name:  "manifest",
		Short: "Post manifest ledger: manifest <init|ensure-clip|append-post|count-scheduled|find-clip|conflicts|schedule-ids|list-by-channel|expire> --job <jobdir>",
		Run: func(args []string) int {
			return manifest.Main(args, os.Stdin, os.Stdout, os.Stderr)
		},
	})
}
