package cli

import (
	"flag"
	"fmt"
	"os"

	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/notify"
)

func init() {
	Register(Command{Name: "notify", Short: "Send a job event to macOS Notification Center and Slack (never fails)", Run: runNotify})
}

func runNotify(args []string) int {
	fs := flag.NewFlagSet("notify", flag.ContinueOnError)
	jobDir := fs.String("job", "", "job directory (required)")
	event := fs.String("event", "", "event name, e.g. staged, clipped, drafted (required)")
	message := fs.String("message", "", "free text")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if *jobDir == "" || *event == "" || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: longform notify --job <jobdir> --event <string> [--message <text>]")
		return 64
	}
	j, err := model.LoadJob(*jobDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "notify:", err)
		return 65
	}
	p, err := model.LoadProfile(j.Profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "notify: profile:", err, "(macOS notification skipped)")
		p = &model.Profile{Name: j.Profile}
	}
	if err := notify.Send(p, j, *event, *message); err != nil {
		fmt.Fprintln(os.Stderr, "notify:", err)
	}
	return 0
}
