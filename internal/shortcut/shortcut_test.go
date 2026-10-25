package shortcut

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuildCarriesProfileAndBrand(t *testing.T) {
	data, err := Build(Spec{Profile: "evc", Brand: "Enterprise & Vibe"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		"<string>longform-inbox/evc/</string>",
		"Sent to Enterprise &amp; Vibe inbox",
		"is.workflow.actions.documentpicker.save",
		"is.workflow.actions.notification",
		"<string>ActionExtension</string>",
		"WFAVAssetContentItem",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("plist lacks %q", want)
		}
	}
	if strings.Contains(s, "<key>WFAskWhereToSave</key>\n\t\t\t\t<true/>") {
		t.Error("Ask Where to Save must be off")
	}
}

func TestBuildRejectsBadProfile(t *testing.T) {
	for _, p := range []string{"", "a/b", "a b"} {
		if _, err := Build(Spec{Profile: p}); err == nil {
			t.Errorf("profile %q accepted", p)
		}
		if _, err := Build(Spec{Profile: p, Kind: KindMark, Album: "longform-evc"}); err == nil {
			t.Errorf("mark: profile %q accepted", p)
		}
	}
}

// TestSendWorkflowUnchanged pins the send kind byte for byte to the output of
// the template before the mark kind existed (sha256 recorded from HEAD).
func TestSendWorkflowUnchanged(t *testing.T) {
	for _, tc := range []struct {
		spec Spec
		sum  string
	}{
		{Spec{Profile: "evc", Brand: "EVC"}, "63f610c7059af9e401a6c00affad487669c8dab41b0d2bebf18c3fb2464b96f7"},
		{Spec{Profile: "evc", Brand: "EVC", Kind: KindSend}, "63f610c7059af9e401a6c00affad487669c8dab41b0d2bebf18c3fb2464b96f7"},
		{Spec{Profile: "evc", Brand: "Enterprise & Vibe"}, "4d9a9b68528c3d110cadc1c59d32e5a8c627c4a012d2b43eaddb4af01555dfbd"},
	} {
		data, err := Build(tc.spec)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != tc.sum {
			t.Errorf("%+v: send workflow changed: sha256 %s, want %s", tc.spec, got, tc.sum)
		}
	}
}

func TestBuildKinds(t *testing.T) {
	cases := []struct {
		name    string
		spec    Spec
		title   string
		want    []string
		notWant []string
		wantErr string
	}{
		{
			name:    "send default",
			spec:    Spec{Profile: "evc", Brand: "EVC"},
			title:   "Send to EVC inbox",
			want:    []string{"is.workflow.actions.documentpicker.save", "<string>longform-inbox/evc/</string>", "Sent to EVC inbox"},
			notWant: []string{RemoveFromAlbumActionID, SaveToAlbumActionID, "Marked for"},
		},
		{
			name:  "mark",
			spec:  Spec{Profile: "evc", Brand: "EVC", Kind: KindMark, Album: "longform-evc"},
			title: "Mark for EVC",
			want: []string{
				"<string>is.workflow.actions.removefromalbum</string>",
				"<string>is.workflow.actions.savetocameraroll</string>",
				"<key>WFRemoveAlbumSelectedGroup</key>\n\t\t\t\t<string>longform-evc</string>",
				"<key>WFCameraRollSelectedGroup</key>\n\t\t\t\t<string>longform-evc</string>",
				"<string>ExtensionInput</string>",
				"<string>WFPhotoMediaContentItem</string>",
				"<string>ActionExtension</string>",
				"<string>Marked for EVC</string>",
				"<string>Added to the longform-evc album. The Mac picks it up once iCloud Photos has synced it.</string>",
			},
			notWant: []string{"documentpicker.save", "longform-inbox/", "is.workflow.actions.addtoalbum"},
		},
		{
			name:  "mark escapes brand and album",
			spec:  Spec{Profile: "evc", Brand: "Enterprise & Vibe", Kind: KindMark, Album: "Q&A <raw>"},
			title: "Mark for Enterprise & Vibe",
			want:  []string{"Marked for Enterprise &amp; Vibe", "<string>Q&amp;A &lt;raw&gt;</string>"},
		},
		{
			name:  "mark brand defaults to profile",
			spec:  Spec{Profile: "evc", Kind: KindMark, Album: "longform-evc"},
			title: "Mark for evc",
			want:  []string{"Marked for evc"},
		},
		{name: "mark without album", spec: Spec{Profile: "evc", Kind: KindMark}, wantErr: "album"},
		{name: "mark with blank album", spec: Spec{Profile: "evc", Kind: KindMark, Album: "  "}, wantErr: "album"},
		{name: "unknown kind", spec: Spec{Profile: "evc", Kind: "copy", Album: "x"}, wantErr: `kind "copy"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := Build(tc.spec)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one mentioning %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			n, err := tc.spec.normalize()
			if err != nil {
				t.Fatal(err)
			}
			if got := n.Name(); got != tc.title {
				t.Errorf("Name() = %q, want %q", got, tc.title)
			}
			s := string(data)
			for _, w := range tc.want {
				if !strings.Contains(s, w) {
					t.Errorf("plist lacks %q", w)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(s, w) {
					t.Errorf("plist should not contain %q", w)
				}
			}
		})
	}
}

// TestMarkWorkflowPinned pins the mark kind byte for byte, so any change to
// what the phone runs is deliberate. The bytes are the ones whose actions were
// matched against shared shortcuts and checked against WorkflowKit's import
// allowlist (see the comment on SaveToAlbumActionID).
func TestMarkWorkflowPinned(t *testing.T) {
	for _, tc := range []struct {
		spec Spec
		sum  string
	}{
		{Spec{Profile: "evc", Brand: "EVC", Kind: KindMark, Album: "longform-evc"}, "1db8f6e298bf71b11e0fa31b0f4521babe07ceb60385cff43f7901e1ad49df7b"},
		{Spec{Profile: "evc", Brand: "Enterprise & Vibe", Kind: KindMark, Album: "Q&A <raw>"}, "509d66b0a7ad7b08e57bb0aae4ce3d72c9eab416e2955d08b690e982fe26a701"},
	} {
		data, err := Build(tc.spec)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != tc.sum {
			t.Errorf("%+v: mark workflow changed: sha256 %s, want %s", tc.spec, got, tc.sum)
		}
	}
}

// TestMarkUsesNoAppIntents guards the import: Shortcuts refuses a shortcut
// holding an Apple App Intent missing from its public-action allowlist, and
// Photos' Add Photos To Album intent is missing from all of them. The mark kind
// must stay on classic is.workflow.actions.* actions.
func TestMarkUsesNoAppIntents(t *testing.T) {
	data, err := Build(Spec{Profile: "evc", Brand: "EVC", Kind: KindMark, Album: "longform-evc"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, bad := range []string{"AppIntentDescriptor", "AddAssetsToAlbumIntent", "AlbumEntity", "com.apple.mobileslideshow", "com.apple.Photos"} {
		if strings.Contains(s, bad) {
			t.Errorf("mark plist contains %q", bad)
		}
	}
	remove := strings.Index(s, "<string>"+RemoveFromAlbumActionID+"</string>")
	save := strings.Index(s, "<string>"+SaveToAlbumActionID+"</string>")
	note := strings.Index(s, "<string>is.workflow.actions.notification</string>")
	if !(remove >= 0 && remove < save && save < note) {
		t.Errorf("action order remove=%d save=%d notification=%d, want Remove from Album, Save to Photo Album, Show Notification", remove, save, note)
	}
	if got := strings.Count(s, "<string>ExtensionInput</string>"); got != 2 {
		t.Errorf("Shortcut Input used %d times, want 2 (Remove from Album and Save to Photo Album)", got)
	}
}

// TestMarkActionsMatchShortcutsSerialisation decodes the plist with plutil and
// compares each action with the form Shortcuts itself stores: the album as a
// bare title string and the input as a WFTextTokenAttachment (as in shared
// shortcuts that move existing photos between albums).
func TestMarkActionsMatchShortcutsSerialisation(t *testing.T) {
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil not available")
	}
	data, err := Build(Spec{Profile: "evc", Brand: "EVC", Kind: KindMark, Album: "longform-evc"})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "mark.plist")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("plutil", "-extract", "WFWorkflowActions", "json", "-o", "-", p).Output()
	if err != nil {
		t.Fatalf("plutil -extract: %v", err)
	}
	var actions []map[string]any
	if err := json.Unmarshal(out, &actions); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"Value": map[string]any{"Type": "ExtensionInput"}, "WFSerializationType": "WFTextTokenAttachment"}
	want := []map[string]any{
		{
			"WFWorkflowActionIdentifier": "is.workflow.actions.removefromalbum",
			"WFWorkflowActionParameters": map[string]any{"WFInput": input, "WFRemoveAlbumSelectedGroup": "longform-evc"},
		},
		{
			"WFWorkflowActionIdentifier": "is.workflow.actions.savetocameraroll",
			"WFWorkflowActionParameters": map[string]any{"WFCameraRollSelectedGroup": "longform-evc", "WFInput": input},
		},
		{
			"WFWorkflowActionIdentifier": "is.workflow.actions.notification",
			"WFWorkflowActionParameters": map[string]any{
				"WFNotificationActionTitle": "Marked for EVC",
				"WFNotificationActionBody":  "Added to the longform-evc album. The Mac picks it up once iCloud Photos has synced it.",
				"WFNotificationActionSound": true,
			},
		},
	}
	if !reflect.DeepEqual(actions, want) {
		got, _ := json.MarshalIndent(actions, "", "  ")
		exp, _ := json.MarshalIndent(want, "", "  ")
		t.Errorf("mark actions:\n%s\nwant:\n%s", got, exp)
	}
}

func TestNameByKind(t *testing.T) {
	if got := (Spec{Brand: "EVC"}).Name(); got != "Send to EVC inbox" {
		t.Errorf("default Name() = %q", got)
	}
	if got := (Spec{Brand: "EVC", Kind: KindMark}).Name(); got != "Mark for EVC" {
		t.Errorf("mark Name() = %q", got)
	}
}

func TestBuildIsValidPlistWhenPlutilExists(t *testing.T) {
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil not available")
	}
	for _, spec := range []Spec{
		{Profile: "sandbox-bjj", Brand: "Sandbox BJJ"},
		{Profile: "sandbox-bjj", Brand: "Sandbox BJJ", Kind: KindMark, Album: "longform-sandbox-bjj"},
	} {
		data, err := Build(spec)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(t.TempDir(), "x.shortcut")
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("plutil", "-lint", p).CombinedOutput(); err != nil {
			t.Fatalf("%s: plutil -lint: %v: %s", spec.Kind, err, out)
		}
	}
}

func TestSignWhenShortcutsToolExists(t *testing.T) {
	if os.Getenv("LONGFORM_INTEGRATION") == "" {
		t.Skip("set LONGFORM_INTEGRATION=1: signing needs a Mac signed into iCloud")
	}
	if _, err := exec.LookPath("shortcuts"); err != nil {
		t.Skip("shortcuts tool not available")
	}
	for _, spec := range []Spec{
		{Profile: "evc", Brand: "EVC"},
		{Profile: "evc", Brand: "EVC", Kind: KindMark, Album: "longform-evc"},
	} {
		data, _ := Build(spec)
		dir := t.TempDir()
		unsigned := filepath.Join(dir, "u.shortcut")
		signed := filepath.Join(dir, "s.shortcut")
		if err := os.WriteFile(unsigned, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := Sign(unsigned, signed, "anyone"); err != nil {
			t.Fatalf("%s: sign: %v", spec.Name(), err)
		}
		st, err := os.Stat(signed)
		if err != nil || st.Size() <= int64(len(data)) {
			t.Fatalf("%s: signed file missing or not larger than the input: %v", spec.Name(), err)
		}
	}
}
