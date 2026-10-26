package release

import "testing"

func TestParseVersion(t *testing.T) {
	for in, ok := range map[string]bool{
		"v0.2.1": true, "v1.0.0": true, "v10.20.30": true,
		"0.2.1": false, "v0.2": false, "v0.2.1-rc1": false, "v01.2.3": false, "": false, "va.b.c": false,
	} {
		v, err := ParseVersion(in)
		if (err == nil) != ok {
			t.Errorf("ParseVersion(%q) err = %v, want ok=%v", in, err, ok)
		}
		if ok && v.String() != in {
			t.Errorf("ParseVersion(%q).String() = %q", in, v)
		}
	}
}

func TestVersionNext(t *testing.T) {
	v := Version{0, 2, 1}
	for b, want := range map[Bump]string{Patch: "v0.2.2", Minor: "v0.3.0", Major: "v1.0.0", "": "v0.2.2"} {
		if got := v.Next(b).String(); got != want {
			t.Errorf("v0.2.1 + %q = %s, want %s", b, got, want)
		}
	}
}

func TestLatest(t *testing.T) {
	v, ok := Latest([]string{"v0.9.0", "v0.10.0", "v0.2.1", "v1.0.0-rc1", "nightly", "v0.10"})
	if !ok || v.String() != "v0.10.0" {
		t.Errorf("Latest = %s, %v; want v0.10.0 (numeric, not lexical)", v, ok)
	}
	if _, ok := Latest([]string{"nightly"}); ok {
		t.Error("Latest found a version among non-version tags")
	}
}

func TestParseBump(t *testing.T) {
	for in, want := range map[string]Bump{"": "", "auto": "", "patch": Patch, "minor": Minor, "major": Major} {
		if got, err := ParseBump(in); err != nil || got != want {
			t.Errorf("ParseBump(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseBump("huge"); err == nil {
		t.Error("ParseBump(huge) accepted")
	}
}
