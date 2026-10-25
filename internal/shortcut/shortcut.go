// Package shortcut builds the iOS/macOS Shortcut that sends a recording from
// the phone's Share sheet into a profile's iCloud Drive inbox. The file is a
// plist in the Shortcuts "workflow" format; `shortcuts sign` (macOS 12+) turns
// it into an importable, signed .shortcut, and opening that file hands it to
// the Shortcuts app, which syncs it to the phone through iCloud.
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

// Spec is what the shortcut needs to know.
type Spec struct {
	Profile string // folder under iCloud Drive/longform-inbox/
	Brand   string // display name used in the shortcut's name and notification
}

// Name is the shortcut's title as it appears in the Share sheet.
func (s Spec) Name() string { return "Send to " + s.Brand + " inbox" }

// DestinationPath is the iCloud Drive folder, relative to the Drive root.
func (s Spec) DestinationPath() string { return "longform-inbox/" + s.Profile }

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

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// Build renders the unsigned workflow plist.
func Build(s Spec) ([]byte, error) {
	if s.Profile == "" {
		return nil, fmt.Errorf("shortcut: profile is required")
	}
	if strings.ContainsAny(s.Profile, "/\\ ") {
		return nil, fmt.Errorf("shortcut: profile %q must be a plain folder name", s.Profile)
	}
	if s.Brand == "" {
		s.Brand = s.Profile
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, s); err != nil {
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
