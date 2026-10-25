package cli

import (
	"flag"
	"fmt"
	"os"

	"github.com/michaellady/longform-pipeline/internal/model"
	"github.com/michaellady/longform-pipeline/internal/shortcut"
)

func init() {
	Register(Command{Name: "shortcut", Short: "Build and sign the iPhone Share-sheet shortcut for a profile: shortcut --profile <p> [--kind send|mark] [--album <name>] [--brand <b>] [--open]", Run: runShortcut})
}

func runShortcut(args []string) int {
	fs := flag.NewFlagSet("shortcut", flag.ContinueOnError)
	profile := fs.String("profile", "", "profile name (the inbox folder under iCloud Drive/longform-inbox/)")
	brand := fs.String("brand", "", "display name for the shortcut (default: the profile's brand.display)")
	kind := fs.String("kind", shortcut.KindSend, "send: \"Send to <Brand> inbox\" copies the file into the iCloud Drive inbox; mark: \"Mark for <Brand>\" adds the Photos item to the profile's Photos album")
	album := fs.String("album", "", "Photos album for --kind mark (default: the profile's photos.album)")
	open := fs.Bool("open", false, "hand the signed file to the Shortcuts app for import")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	if *profile == "" {
		fmt.Fprintln(os.Stderr, "shortcut: --profile is required")
		return 64
	}
	switch *kind {
	case shortcut.KindSend:
		if *album != "" {
			fmt.Fprintln(os.Stderr, "shortcut: --album only applies to --kind mark")
			return 64
		}
	case shortcut.KindMark:
	default:
		fmt.Fprintf(os.Stderr, "shortcut: --kind must be %s or %s, not %q\n", shortcut.KindSend, shortcut.KindMark, *kind)
		return 64
	}
	spec := shortcut.Spec{Profile: *profile, Brand: *brand, Kind: *kind, Album: *album}
	var prof *model.Profile
	var profErr error
	if spec.Brand == "" || (spec.Kind == shortcut.KindMark && spec.Album == "") {
		prof, profErr = model.LoadProfile(*profile)
	}
	if spec.Brand == "" {
		if profErr == nil && prof != nil && prof.Brand.Display != "" {
			spec.Brand = prof.Brand.Display
		} else {
			spec.Brand = *profile
		}
	}
	if spec.Kind == shortcut.KindMark && spec.Album == "" {
		if profErr == nil && prof != nil {
			spec.Album = prof.Photos.Album
		}
		if spec.Album == "" {
			fmt.Fprintln(os.Stderr, "shortcut: --kind mark needs an album (--album or the profile's photos.album)")
			if profErr != nil {
				fmt.Fprintln(os.Stderr, "shortcut:", profErr)
			}
			return 64
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
	if spec.Kind == shortcut.KindMark {
		fmt.Fprintf(os.Stderr, "shortcut: create the %q album on the phone first (Photos > Albums > + > New Album); the shortcut finds it by name.\n", spec.Album)
		fmt.Fprintf(os.Stderr, "shortcut: the zero-copy route is Photos' own Share > Add to Album > %q. A shortcut run from the Share sheet makes Photos export the whole video first (\"Preparing...\"), which takes minutes for a long recording; use the shortcut only when that wait is acceptable.\n", spec.Album)
	}
	return 0
}
