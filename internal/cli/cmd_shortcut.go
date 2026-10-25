package cli

import (
	"flag"
	"fmt"
	"os"

	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/shortcut"
)

func init() {
	Register(Command{Name: "shortcut", Short: "Build and sign the iPhone Share-sheet shortcut for a profile: shortcut --profile <p> [--open]", Run: runShortcut})
}

func runShortcut(args []string) int {
	fs := flag.NewFlagSet("shortcut", flag.ContinueOnError)
	profile := fs.String("profile", "", "profile name (the inbox folder under iCloud Drive/longform-inbox/)")
	brand := fs.String("brand", "", "display name for the shortcut (default: the profile's brand.display)")
	open := fs.Bool("open", false, "hand the signed file to the Shortcuts app for import")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if *profile == "" {
		fmt.Fprintln(os.Stderr, "shortcut: --profile is required")
		return 64
	}
	spec := shortcut.Spec{Profile: *profile, Brand: *brand}
	if spec.Brand == "" {
		if p, err := model.LoadProfile(*profile); err == nil && p.Brand.Display != "" {
			spec.Brand = p.Brand.Display
		} else {
			spec.Brand = *profile
		}
	}
	path, err := shortcut.Write(spec, *open)
	if err != nil {
		fmt.Fprintln(os.Stderr, "shortcut:", err)
		if path != "" {
			fmt.Fprintln(os.Stderr, "unsigned workflow left at", path)
		}
		return 70
	}
	fmt.Println(path)
	if *open {
		fmt.Fprintln(os.Stderr, "shortcut: the Shortcuts app is showing an Add dialog; confirm it once and iCloud syncs the shortcut to the phone.")
	}
	return 0
}
