package manifest

import "github.com/michaellady/longform-pipeline/internal/gates"

// CellMatch is what MatchCells found for one cell.
type CellMatch struct {
	Parked  bool           // held or skipped: needs no post and is never matched
	Post    *ScheduledPost // the recorded post that stands for the cell; nil when none
	Errored string         // error of an unmatched same clip/platform post whose call failed
	NoID    bool           // an unmatched same clip/platform post is recorded without an id
}

// PostPlatform is the platform a recorded post went to: its platform field,
// else the one its label names (posts appended before platform existed).
func (p ScheduledPost) PostPlatform() string {
	if p.Platform != "" {
		return p.Platform
	}
	return gates.LabelToPlatform(p.Label)
}

// MatchCells pairs every cell that should post (not parked) with one
// recorded post of the same clip id and platform that has an id and no
// error, in cell order. Each post satisfies at most one cell, so a double
// append never hides a missing cell. This is the one identity rule:
// `manifest seal` checks it and `opus schedule --job` skips by it. The
// returned slice is parallel to cells; Post points into m.
func (m *Manifest) MatchCells(cells []gates.Cell) []CellMatch {
	type ref struct{ clip, post int }
	used := map[ref]bool{}
	out := make([]CellMatch, len(cells))
	for i, c := range cells {
		if c.Parked() {
			out[i].Parked = true
			continue
		}
		platform := gates.PlatformOf(c)
	clips:
		for ci := range m.Clips {
			if m.Clips[ci].ClipID != c.ClipID {
				continue
			}
			for pi := range m.Clips[ci].ScheduledPosts {
				p := &m.Clips[ci].ScheduledPosts[pi]
				if p.PostPlatform() != platform || used[ref{ci, pi}] {
					continue
				}
				switch {
				case p.Error != "":
					out[i].Errored = p.Error
				case p.ScheduleID == "":
					out[i].NoID = true
				default:
					used[ref{ci, pi}] = true
					out[i].Post = p
					break clips
				}
			}
		}
	}
	return out
}

// RecordedID is the id AppendScheduleLine records for the line: post_id,
// else schedule_id (unless the call failed), else response.data.scheduleId.
// Empty for a failed call or a Buffer line without post_id.
func (l ScheduleLine) RecordedID() string {
	if l.PostID != "" {
		return l.PostID
	}
	if l.Error == "" && l.ScheduleID != "" {
		return l.ScheduleID
	}
	return ExtractScheduleID(l.Response)
}

// HasScheduleID reports whether any recorded post carries id (stored, or
// extractable from its response like ScheduleIDs).
func (m *Manifest) HasScheduleID(id string) bool {
	if id == "" {
		return false
	}
	for _, have := range m.ScheduleIDs() {
		if have == id {
			return true
		}
	}
	return false
}

// RecordLine appends one schedule line to the manifest at path and saves it
// at once (`opus schedule --job` calls it right after each successful POST,
// so a crash later in the run cannot lose the post). The manifest is created
// as `manifest init` would when absent; a line whose id is already recorded
// is not appended again and RecordLine reports false.
func RecordLine(path string, l ScheduleLine) (bool, error) {
	if _, err := Init(path, InitOptions{}); err != nil {
		return false, err
	}
	m, err := Load(path)
	if err != nil {
		return false, err
	}
	if m.HasScheduleID(l.RecordedID()) {
		return false, nil
	}
	if err := m.AppendScheduleLine(l); err != nil {
		return false, err
	}
	return true, m.Save(path)
}
