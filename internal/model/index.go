package model

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Index is the dedupe table: content hash -> job id. It lives at
// JobsRoot()/jobs.json and is the only cross-job state the watcher keeps.
type Index struct {
	SchemaVersion string            `json:"schema_version"`
	ByHash        map[string]string `json:"by_hash"` // sha256 -> job id
	UpdatedAt     time.Time         `json:"updated_at"`
}

func indexPath() string { return filepath.Join(JobsRoot(), "jobs.json") }

// LoadIndex reads the index, returning an empty one when absent.
func LoadIndex() (*Index, error) {
	b, err := os.ReadFile(indexPath())
	if errors.Is(err, os.ErrNotExist) {
		return &Index{SchemaVersion: "1", ByHash: map[string]string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var ix Index
	if err := json.Unmarshal(b, &ix); err != nil {
		return nil, err
	}
	if ix.ByHash == nil {
		ix.ByHash = map[string]string{}
	}
	return &ix, nil
}

// Has reports whether a content hash (file or stitched set) is already a job.
func (ix *Index) Has(hash string) (string, bool) {
	id, ok := ix.ByHash[hash]
	return id, ok
}

// Add records a hash -> job id mapping and saves.
func (ix *Index) Add(hash, jobID string) error {
	ix.ByHash[hash] = jobID
	return ix.Save()
}

// Save writes the index atomically.
func (ix *Index) Save() error {
	ix.UpdatedAt = time.Now().UTC()
	b, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(indexPath(), b, 0o644)
}
