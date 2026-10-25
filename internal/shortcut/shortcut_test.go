package shortcut

import (
	"os"
	"os/exec"
	"path/filepath"
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
	}
}

func TestBuildIsValidPlistWhenPlutilExists(t *testing.T) {
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil not available")
	}
	data, err := Build(Spec{Profile: "sandbox-bjj", Brand: "Sandbox BJJ"})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "x.shortcut")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("plutil", "-lint", p).CombinedOutput(); err != nil {
		t.Fatalf("plutil -lint: %v: %s", err, out)
	}
}

func TestSignWhenShortcutsToolExists(t *testing.T) {
	if os.Getenv("LONGFORM_INTEGRATION") == "" {
		t.Skip("set LONGFORM_INTEGRATION=1: signing needs a Mac signed into iCloud")
	}
	if _, err := exec.LookPath("shortcuts"); err != nil {
		t.Skip("shortcuts tool not available")
	}
	data, _ := Build(Spec{Profile: "evc", Brand: "EVC"})
	dir := t.TempDir()
	unsigned := filepath.Join(dir, "u.shortcut")
	signed := filepath.Join(dir, "s.shortcut")
	if err := os.WriteFile(unsigned, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Sign(unsigned, signed, "anyone"); err != nil {
		t.Fatalf("sign: %v", err)
	}
	st, err := os.Stat(signed)
	if err != nil || st.Size() <= int64(len(data)) {
		t.Fatalf("signed file missing or not larger than the input: %v", err)
	}
}
