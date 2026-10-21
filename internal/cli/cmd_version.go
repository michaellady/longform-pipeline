package cli

import "fmt"

// Version is set at build time with -ldflags "-X ...cli.Version=v1.2.3".
var Version = "dev"

func init() {
	Register(Command{Name: "version", Short: "Print the binary version", Run: func(args []string) int {
		fmt.Println("longform", Version)
		return 0
	}})
}
