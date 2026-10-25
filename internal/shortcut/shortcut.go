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

// Identifiers the mark kind uses. "Add to Album" is not a classic
// is.workflow.actions.* action (is.workflow.actions.addtoalbum does not exist;
// WorkflowKit reports it as "Unknown Action"). It is the Photos App Intent
// AddAssetsToAlbumIntent (PhotosUICore's Metadata.appintents: parameters
// "assets" [AssetEntity] and "album" AlbumEntity). Shortcuts files name Photos
// intents by the iOS bundle id: WorkflowKit's exported constants read
// WFLinkActionIdentifierPhotosDeleteAlbumsIntent =
// "com.apple.mobileslideshow.DeleteAlbumsIntent" and
// WFLinkActionIdentifierPhotosFindAlbum = "com.apple.mobileslideshow.AlbumEntity"
// (macOS 27, probed with dlsym). The album parameter holds an AlbumEntity,
// whose fixed value is a device-local id the Mac cannot know, so the shortcut
// finds the album by name on the phone ("Find Albums where Name is <album>")
// and passes that action's output to Add to Album.
const (
	PhotosBundleID        = "com.apple.mobileslideshow"
	AddToAlbumActionID    = PhotosBundleID + ".AddAssetsToAlbumIntent"
	FindAlbumsActionID    = PhotosBundleID + ".AlbumEntity"
	findAlbumsUUID        = "5E1C6A0B-2F4D-4B8E-9A37-0C5D8F1E2A61"
	addToAlbumUUID        = "9B7E3D52-6A1C-4F08-B2D4-7E6F0A3C1D95"
	photoMediaContentItem = "WFPhotoMediaContentItem" // a PHAsset from the Photos share sheet
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

// markTmpl is the "Mark for <Brand>" workflow: Find Albums (Name is <album>,
// limit 1) -> Add to Album (assets = Shortcut Input, album = Find Albums'
// output) -> Show Notification. The App Intent actions follow the form of
// Apple's own Gallery workflows (WorkflowKit Gallery.bundle: Find Notes is
// is.workflow.actions.filter.notes + AppIntentDescriptor; Summarize Text is
// <bundle>.<IntentIdentifier>). WFPhotoMediaContentItem is listed first so the
// Photos share sheet hands over the PHAsset itself, which WorkflowKit coerces
// to the intent's AssetEntity (WFPhotoMediaContentItem +linkEntityCoercionHandler).
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
			<string>` + FindAlbumsActionID + `</string>
			<key>WFWorkflowActionParameters</key>
			<dict>
				<key>AppIntentDescriptor</key>
				<dict>
					<key>TeamIdentifier</key>
					<string>0000000000</string>
					<key>BundleIdentifier</key>
					<string>` + PhotosBundleID + `</string>
					<key>Name</key>
					<string>Photos</string>
					<key>AppIntentIdentifier</key>
					<string>AlbumEntity</string>
					<key>ActionRequiresAppInstallation</key>
					<true/>
				</dict>
				<key>WFContentItemFilter</key>
				<dict>
					<key>Value</key>
					<dict>
						<key>WFActionParameterFilterPrefix</key>
						<integer>1</integer>
						<key>WFContentPredicateBoundedDate</key>
						<false/>
						<key>WFActionParameterFilterTemplates</key>
						<array>
							<dict>
								<key>Operator</key>
								<integer>4</integer>
								<key>Property</key>
								<string>Name</string>
								<key>Removable</key>
								<true/>
								<key>Values</key>
								<dict>
									<key>Unit</key>
									<integer>4</integer>
									<key>String</key>
									<string>{{x .Album}}</string>
								</dict>
							</dict>
						</array>
					</dict>
					<key>WFSerializationType</key>
					<string>WFContentPredicateTableTemplate</string>
				</dict>
				<key>WFContentItemLimitEnabled</key>
				<true/>
				<key>WFContentItemLimitNumber</key>
				<real>1</real>
				<key>UUID</key>
				<string>` + findAlbumsUUID + `</string>
			</dict>
		</dict>
		<dict>
			<key>WFWorkflowActionIdentifier</key>
			<string>` + AddToAlbumActionID + `</string>
			<key>WFWorkflowActionParameters</key>
			<dict>
				<key>AppIntentDescriptor</key>
				<dict>
					<key>TeamIdentifier</key>
					<string>0000000000</string>
					<key>BundleIdentifier</key>
					<string>` + PhotosBundleID + `</string>
					<key>Name</key>
					<string>Photos</string>
					<key>AppIntentIdentifier</key>
					<string>AddAssetsToAlbumIntent</string>
					<key>ActionRequiresAppInstallation</key>
					<true/>
				</dict>
				<key>assets</key>
				<dict>
					<key>Value</key>
					<dict>
						<key>Type</key>
						<string>ExtensionInput</string>
					</dict>
					<key>WFSerializationType</key>
					<string>WFTextTokenAttachment</string>
				</dict>
				<key>album</key>
				<dict>
					<key>Value</key>
					<dict>
						<key>OutputName</key>
						<string>Albums</string>
						<key>OutputUUID</key>
						<string>` + findAlbumsUUID + `</string>
						<key>Type</key>
						<string>ActionOutput</string>
					</dict>
					<key>WFSerializationType</key>
					<string>WFTextTokenAttachment</string>
				</dict>
				<key>UUID</key>
				<string>` + addToAlbumUUID + `</string>
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
