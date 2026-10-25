package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/michaellady/longform-pipeline/internal/ingest"
	"github.com/michaellady/longform-pipeline/internal/model"
)

func init() {
	Register(Command{Name: "launch-session", Short: "Start the claude editorial session for a clipped job (once)", Run: runLaunchSession})
}

func runLaunchSession(args []string) int {
	fs := flag.NewFlagSet("launch-session", flag.ContinueOnError)
	force := fs.Bool("force", false, "launch even if a session was already launched or the job is not clipped")
	claude := fs.String("claude", "", "path to the claude binary (default: $LONGFORM_CLAUDE, PATH, ~/.local/bin/claude)")
	modelName := fs.String("model", "", "claude --model for the session (default: $"+ingest.ClaudeModelEnv+", else "+ingest.DefaultClaudeModel+")")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: longform launch-session [--force] [--claude <path>] [--model <name>] <jobdir>")
		return 64
	}
	j, err := model.LoadJob(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "launch-session:", err)
		return 65
	}
	launched, err := ingest.LaunchSession(j, ingest.LaunchOptions{Claude: *claude, Model: *modelName, Force: *force})
	if err != nil {
		fmt.Fprintln(os.Stderr, "launch-session:", err)
		if errors.Is(err, ingest.ErrNotClipped) {
			return 65
		}
		return 70
	}
	if !launched {
		fmt.Printf("session already launched at %v (pid %v)\n", j.Extra[ingest.SessionLaunchedKey], j.Extra["session_pid"])
		return 0
	}
	fmt.Printf("session launched pid %v, model %v, stdout %v\n", j.Extra["session_pid"], j.Extra[ingest.SessionModelKey], j.Extra["session_stdout"])
	return 0
}
