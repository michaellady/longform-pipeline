package cli

import (
	"os"

	"github.com/michaellady/longform-pipeline/internal/slots"
)

func init() {
	Register(Command{
		Name:  "slots",
		Short: "Cadence slot math: slots propose --profile <p> --cells <file> --existing <file> [--from YYYY-MM-DD] [--approved-at RFC3339]",
		Run: func(args []string) int {
			return slots.Main(args, os.Stdin, os.Stdout, os.Stderr)
		},
	})
}
