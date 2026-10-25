// Package shortcut builds the iOS/macOS Share-sheet Shortcuts for a profile.
// There are two kinds:
//
//   - send ("Send to <Brand> inbox"): copies the shared file into the
//     profile's iCloud Drive inbox with Save File. Works for any file, but a
//     long 4K recording is copied byte for byte through Shortcuts.
//   - mark ("Mark for <Brand>"): adds the shared Photos item to the profile's
//     Photos album (profile.photos.album). Nothing is copied; iCloud Photos
//     syncs the original and the Mac's Photos feed picks it up.
//
// The file is a plist in the Shortcuts "workflow" format; `shortcuts sign`
// (macOS 12+) turns it into an importable, signed .shortcut, and opening that
// file hands it to the Shortcuts app, which syncs it to the phone through
// iCloud.
package shortcut

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/michaellady/longform-pipeline/internal/model"
)

// Shortcut kinds.
const (
	KindSend = "send" // Save File into the iCloud Drive inbox (default)
	KindMark = "mark" // Add to the profile's Photos album
)

// Actions the mark kind uses: the classic Photos actions, never the Photos App
// Intents. The "Add Photos To Album" intent (<bundle>.AddAssetsToAlbumIntent)
// cannot be imported: Shortcuts refuses any shortcut holding an Apple App
// Intent that is missing from WorkflowKit's public-action allowlist
// (Resources/AssistantIntents-{mac,iPhone,iPad}.metadata, md5 of
// "<bundle>.<intent>"; "can't be imported because it contains features not
// supported on this device"), and AddAssetsToAlbumIntent is in none of them on
// OS 27 (macOS 27.0.1, WorkflowKit 5037.0.19) nor on iOS 26.0.
//
// Save to Photo Album (is.workflow.actions.savetocameraroll) is how real
// shortcuts put existing photos into an album. Its album parameter is the album
// title as a plain string, which Shortcuts matches by name on whichever device
// runs it. Given a Photos item (WFPhotoMediaContentItem) that is not yet in the
// album it adds that PHAsset to the album (PHAssetCollectionChangeRequest
// addAssets:) and copies nothing; given one that is already in the album it
// saves a second copy. Remove from Album first makes a repeated mark harmless:
// it is a no-op for an item not in the album, and afterwards Save to Photo
// Album adds the original back instead of copying it.
const (
	RemoveFromAlbumActionID = "is.workflow.actions.removefromalbum"
	SaveToAlbumActionID     = "is.workflow.actions.savetocameraroll"
	photoMediaContentItem   = "WFPhotoMediaContentItem" // a PHAsset from the Photos share sheet
)

// Spec is what the shortcut needs to know.
type Spec struct {
	Profile string // folder under iCloud Drive/longform-inbox/
	Brand   string // display name used in the shortcut's name and notification
	Kind    string // KindSend (default when empty) or KindMark
	Album   string // Photos album the mark kind adds to; required for KindMark
}

// Name is the shortcut's title as it appears in the Share sheet.
func (s Spec) Name() string {
	if s.Kind == KindMark {
		return "Mark for " + s.Brand
	}
	return "Send to " + s.Brand + " inbox"
}

// normalize fills defaults and validates.
func (s Spec) normalize() (Spec, error) {
	if s.Profile == "" {
		return s, fmt.Errorf("shortcut: profile is required")
	}
	if strings.ContainsAny(s.Profile, "/\\ ") {
		return s, fmt.Errorf("shortcut: profile %q must be a plain folder name", s.Profile)
	}
	if s.Brand == "" {
		s.Brand = s.Profile
	}
	switch s.Kind {
	case "":
		s.Kind = KindSend
	case KindSend, KindMark:
	default:
		return s, fmt.Errorf("shortcut: kind %q must be %q or %q", s.Kind, KindSend, KindMark)
	}
	if s.Kind == KindMark && strings.TrimSpace(s.Album) == "" {
		return s, fmt.Errorf("shortcut: kind %q needs a Photos album name", KindMark)
	}
	return s, nil
}

// DestinationPath is the folder the Save File action writes into. Shortcuts
// resolves it inside iCloud Drive's "Shortcuts" folder (on a Mac that is
// ~/Library/Mobile Documents/iCloud~is~workflow~my~workflows/Documents), and
// the trailing slash matters: without it the last segment becomes the file
// name, which is how the first run produced "longform-inbox/evc.mov".
func (s Spec) DestinationPath() string { return "longform-inbox/" + s.Profile + "/" }

// tmpl is the "Send to <Brand> inbox" workflow: Save File -> Show Notification.
var tmpl = template.Must(template.New("workflow").Funcs(template.FuncMap{"x": xmlEscape}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>WFWorkflowClientVersion</key>
	<string>2607.0.3</string>
	<key>WFWorkflowMinimumClientVersion</key>
	<integer>900</integer>
	<key>WFWorkflowMinimumClientVersionString</key>
	<string>900</string>
	<key>WFWorkflowIcon</key>
	<dict>
		<key>WFWorkflowIconStartColor</key>
		<integer>4282601983</integer>
		<key>WFWorkflowIconGlyphNumber</key>
		<integer>59511</integer>
	</dict>
	<key>WFWorkflowImportQuestions</key>
	<array/>
	<key>WFWorkflowTypes</key>
	<array>
		<string>ActionExtension</string>
	</array>
	<key>WFWorkflowInputContentItemClasses</key>
	<array>
		<string>WFAVAssetContentItem</string>
		<string>WFImageContentItem</string>
		<string>WFGenericFileContentItem</string>
	</array>
	<key>WFWorkflowHasShortcutInputVariables</key>
	<true/>
	<key>WFWorkflowNoInputBehavior</key>
	<dict>
		<key>Name</key>
		<string>WFWorkflowNoInputBehaviorShowError</string>
		<key>Parameters</key>
		<dict>
			<key>Error</key>
			<string>Share a video to this shortcut to send it to the {{x .Profile}} inbox.</string>
		</dict>
	</dict>
	<key>WFWorkflowActions</key>
	<array>
		<dict>
			<key>WFWorkflowActionIdentifier</key>
			<string>is.workflow.actions.documentpicker.save</string>
			<key>WFWorkflowActionParameters</key>
			<dict>
				<key>WFAskWhereToSave</key>
				<false/>
				<key>WFFileDestinationPath</key>
				<string>{{x .DestinationPath}}</string>
				<key>WFSaveFileOverwrite</key>
				<true/>
				<key>WFInput</key>
				<dict>
					<key>Value</key>
					<dict>
						<key>Type</key>
						<string>ExtensionInput</string>
					</dict>
					<key>WFSerializationType</key>
					<string>WFTextTokenAttachment</string>
				</dict>
			</dict>
		</dict>
		<dict>
			<key>WFWorkflowActionIdentifier</key>
			<string>is.workflow.actions.notification</string>
			<key>WFWorkflowActionParameters</key>
			<dict>
				<key>WFNotificationActionTitle</key>
				<string>Sent to {{x .Brand}} inbox</string>
				<key>WFNotificationActionBody</key>
				<string>Saved to iCloud Drive/{{x .DestinationPath}}. The Mac takes it from here.</string>
				<key>WFNotificationActionSound</key>
				<true/>
			</dict>
		</dict>
	</array>
</dict>
</plist>
`))

// markTmpl is the "Mark for <Brand>" workflow: Remove from Album (<album>,
// Shortcut Input) -> Save to Photo Album (<album>, Shortcut Input) -> Show
// Notification. The two Photos actions are serialised exactly as Shortcuts
// stores them (album = title string, input = WFTextTokenAttachment), which is
// also the form shared shortcuts that move existing photos between albums use.
// They carry no UUIDs because no action reads another's output, as in the
// send kind. WFPhotoMediaContentItem is an accepted input class so the Photos
// share sheet hands over the PHAsset itself rather than a file copy of it.
var markTmpl = template.Must(template.New("mark").Funcs(template.FuncMap{"x": xmlEscape}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>WFWorkflowClientVersion</key>
	<string>2607.0.3</string>
	<key>WFWorkflowMinimumClientVersion</key>
	<integer>900</integer>
	<key>WFWorkflowMinimumClientVersionString</key>
	<string>900</string>
	<key>WFWorkflowIcon</key>
	<dict>
		<key>WFWorkflowIconStartColor</key>
		<integer>4282601983</integer>
		<key>WFWorkflowIconGlyphNumber</key>
		<integer>59511</integer>
	</dict>
	<key>WFWorkflowImportQuestions</key>
	<array/>
	<key>WFWorkflowTypes</key>
	<array>
		<string>ActionExtension</string>
	</array>
	<key>WFWorkflowInputContentItemClasses</key>
	<array>
		<string>` + photoMediaContentItem + `</string>
		<string>WFAVAssetContentItem</string>
		<string>WFImageContentItem</string>
	</array>
	<key>WFWorkflowHasShortcutInputVariables</key>
	<true/>
	<key>WFWorkflowNoInputBehavior</key>
	<dict>
		<key>Name</key>
		<string>WFWorkflowNoInputBehaviorShowError</string>
		<key>Parameters</key>
		<dict>
			<key>Error</key>
			<string>Share a video from Photos to this shortcut to add it to the {{x .Album}} album.</string>
		</dict>
	</dict>
	<key>WFWorkflowActions</key>
	<array>
		<dict>
			<key>WFWorkflowActionIdentifier</key>
			<string>` + RemoveFromAlbumActionID + `</string>
			<key>WFWorkflowActionParameters</key>
			<dict>
				<key>WFInput</key>
				<dict>
					<key>Value</key>
					<dict>
						<key>Type</key>
						<string>ExtensionInput</string>
					</dict>
					<key>WFSerializationType</key>
					<string>WFTextTokenAttachment</string>
				</dict>
				<key>WFRemoveAlbumSelectedGroup</key>
				<string>{{x .Album}}</string>
			</dict>
		</dict>
		<dict>
			<key>WFWorkflowActionIdentifier</key>
			<string>` + SaveToAlbumActionID + `</string>
			<key>WFWorkflowActionParameters</key>
			<dict>
				<key>WFCameraRollSelectedGroup</key>
				<string>{{x .Album}}</string>
				<key>WFInput</key>
				<dict>
					<key>Value</key>
					<dict>
						<key>Type</key>
						<string>ExtensionInput</string>
					</dict>
					<key>WFSerializationType</key>
					<string>WFTextTokenAttachment</string>
				</dict>
			</dict>
		</dict>
		<dict>
			<key>WFWorkflowActionIdentifier</key>
			<string>is.workflow.actions.notification</string>
			<key>WFWorkflowActionParameters</key>
			<dict>
				<key>WFNotificationActionTitle</key>
				<string>Marked for {{x .Brand}}</string>
				<key>WFNotificationActionBody</key>
				<string>Added to the {{x .Album}} album. The Mac picks it up once iCloud Photos has synced it.</string>
				<key>WFNotificationActionSound</key>
				<true/>
			</dict>
		</dict>
	</array>
</dict>
</plist>
`))

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// Build renders the unsigned workflow plist for the spec's kind.
func Build(s Spec) ([]byte, error) {
	s, err := s.normalize()
	if err != nil {
		return nil, err
	}
	t := tmpl
	if s.Kind == KindMark {
		t = markTmpl
	}
	var b bytes.Buffer
	if err := t.Execute(&b, s); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Dir is where built shortcuts are kept: <ConfigRoot>/shortcuts.
func Dir() string { return filepath.Join(model.ConfigRoot(), "shortcuts") }

// Sign turns an unsigned workflow into an importable .shortcut using the
// macOS `shortcuts` tool. mode is "anyone" (shareable) or "people-who-know-me".
func Sign(unsigned, signed, mode string) error {
	if _, err := exec.LookPath("shortcuts"); err != nil {
		return fmt.Errorf("shortcut: the `shortcuts` command is not available (macOS 12 or later): %w", err)
	}
	if mode == "" {
		mode = "anyone"
	}
	cmd := exec.Command("shortcuts", "sign", "--mode", mode, "--input", unsigned, "--output", signed)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("shortcuts sign: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Write builds and signs the shortcut for a profile under Dir(), returning the
// signed file's path. With open=true it hands the file to the Shortcuts app
// (which shows an Add dialog; the person confirms once).
func Write(s Spec, open bool) (string, error) {
	s, err := s.normalize()
	if err != nil {
		return "", err
	}
	data, err := Build(s)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return "", err
	}
	unsigned := filepath.Join(Dir(), s.Name()+".unsigned.shortcut")
	signed := filepath.Join(Dir(), s.Name()+".shortcut")
	if err := model.WriteFileAtomic(unsigned, data, 0o644); err != nil {
		return "", err
	}
	if err := Sign(unsigned, signed, "anyone"); err != nil {
		return unsigned, err
	}
	if open {
		if err := exec.Command("open", signed).Run(); err != nil {
			return signed, fmt.Errorf("open %s: %w", signed, err)
		}
	}
	return signed, nil
}
