// Command longform is the deterministic transport layer of the longform-pipeline
// plugin: it watches an inbox, prepares media, talks to OpusClip, enforces
// publishing gates, and hands editorial judgment to a Claude Code skill.
package main

import (
	"os"

	"github.com/michaellady/longform-pipeline/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
