package model

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandHome replaces a leading ~ with the user's home directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}

// ConfigRoot is where profiles, templates, voice corpora and credentials live.
// Override with LONGFORM_CONFIG_DIR (tests use a temp dir).
func ConfigRoot() string {
	if v := os.Getenv("LONGFORM_CONFIG_DIR"); v != "" {
		return v
	}
	return ExpandHome("~/.config/longform-pipeline")
}

// DataRoot is where jobs and the jobs index live. Override with
// LONGFORM_DATA_DIR (tests use a temp dir).
func DataRoot() string {
	if v := os.Getenv("LONGFORM_DATA_DIR"); v != "" {
		return v
	}
	return ExpandHome("~/Library/Application Support/longform-pipeline")
}

// LogRoot is where launchd and session logs go.
func LogRoot() string {
	if v := os.Getenv("LONGFORM_LOG_DIR"); v != "" {
		return v
	}
	return ExpandHome("~/Library/Logs/longform-pipeline")
}

// ProfilesDir holds one <name>.json per brand profile.
func ProfilesDir() string { return filepath.Join(ConfigRoot(), "profiles") }

// JobsRoot holds one directory per job plus jobs.json (the dedupe index).
func JobsRoot() string { return filepath.Join(DataRoot(), "jobs") }
