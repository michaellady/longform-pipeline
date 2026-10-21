package cli

import (
	"os"

	"github.com/michaellady/longform-pipeline/internal/gates"
)

func init() {
	Register(Command{
		Name:  "gate",
		Short: "Deterministic pre-publish gates: gate validate --profile <p> --cells <file> [--existing <file>] [--censor-list <path>]",
		Run: func(args []string) int {
			return gates.Main(args, os.Stdin, os.Stdout, os.Stderr)
		},
	})
}
