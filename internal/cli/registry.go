// Package cli is the subcommand registry for the longform binary. Each
// subsystem registers its commands from its own file (cmd_<name>.go) in an
// init function, so parallel contributors never edit the same file.
package cli

import (
	"fmt"
	"github.com/michaellady/longform-pipeline/internal/model"
	"os"
	"sort"
)

// Command is one subcommand. Run receives the arguments after the command
// name and returns the process exit code. Exit codes follow sysexits: 0 ok,
// 64 usage error, 65 data/validation failure, 70 internal error, 75 temporary
// failure (retry later).
type Command struct {
	Name  string
	Short string
	Run   func(args []string) int
}

var registry = map[string]Command{}

// Register adds a command. Registering the same name twice panics so a merge
// that produces two implementations fails loudly at startup.
func Register(c Command) {
	if _, dup := registry[c.Name]; dup {
		panic("cli: duplicate command " + c.Name)
	}
	registry[c.Name] = c
}

// Run dispatches args[0] to its command. Secrets the wizard stored in the env
// file (LONGFORM_ENV_FILE or <ConfigRoot>/env) are loaded first, because
// launchd passes only the file's path, never its contents.
func Run(args []string) int {
	if _, err := model.LoadEnvFile(); err != nil {
		fmt.Fprintln(os.Stderr, "longform: env file:", err)
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage()
		return 0
	}
	c, ok := registry[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "longform: unknown command %q\n\n", args[0])
		usage()
		return 64
	}
	return c.Run(args[1:])
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: longform <command> [flags]")
	fmt.Fprintln(os.Stderr)
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(os.Stderr, "  %-16s %s\n", n, registry[n].Short)
	}
}
