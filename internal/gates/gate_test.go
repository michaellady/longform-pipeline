package gates

// Tests vendored from claude-social-media-skills/_shared/publish-constraints/
// constraints_test.go (golden mapping, the 2026-05-31 YouTube title bug, the
// caption/title asymmetry, fail-closed) plus the longform checks (b)-(e).

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaellady/longform-pipeline/internal/model"
)

type goldenPair struct {
	Label    string `json:"label"`
	Platform string `json:"platform"`
}

func loadGolden(t *testing.T) []goldenPair {
	t.Helper()
	b, err := os.ReadFile("testdata/label-platform.golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var g []goldenPair
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	return g
}

func TestLabelToPlatform_MatchesGolden(t *testing.T) {
	for _, p := range loadGolden(t) {
		if got := LabelToPlatform(p.Label); got != p.Platform {
			t.Errorf("LabelToPlatform(%q) = %q, want %q", p.Label, got, p.Platform)
		}
	}
}

// Completeness: golden platforms == constraints rows == the Platforms list.
func TestEveryPlatformHasConstraintsRow(t *testing.T) {
	golden := map[string]bool{}
	for _, p := range loadGolden(t) {
		golden[p.Platform] = true
		if _, ok := Constraints[p.Platform]; !ok {
			t.Errorf("golden platform %q has no constraints row", p.Platform)
		}
	}
	for plat := range Constraints {
		if !golden[plat] {
			t.Errorf("constraints row %q is not referenced by any golden label", plat)
		}
	}
	for _, plat := range Platforms {
		if _, ok := Constraints[plat]; !ok {
			t.Errorf("Platforms entry %q has no constraints row", plat)
		}
	}
}

func TestConstraints_RequiredFieldsHaveCaps(t *testing.T) {
	for plat, pc := range Constraints {
		if _, ok := pc.Fields[pc.PostTextField]; !ok {
			t.Errorf("%s: post_text_field %q is not a declared field", plat, pc.PostTextField)
		}
		for name, fc := range pc.Fields {
			if fc.Required && fc.MaxRunes <= 0 {
				t.Errorf("%s.%s is required but has no positive max_runes", plat, name)
			}
		}
	}
}

func hasVerdict(vs []Verdict, platform, field, reasonSubstr string) bool {
	for _, v := range vs {
		if v.Platform == platform && v.Field == field && strings.Contains(v.Reason, reasonSubstr) {
			return true
		}
	}
	return false
}

// THE bug: the long caption passed as the YouTube title (214 chars) must fail.
func TestCaps_YouTubeOverLongTitleFails(t *testing.T) {
	long := strings.Repeat("a", 214)
	vs := validateCaps(Cell{ClipID: "X", Platform: "youtube", Title: long, Description: long})
	if !hasVerdict(vs, "youtube", "title", "exceeds") {
		t.Fatalf("expected youtube title over-cap verdict, got %+v", vs)
	}
}

// The asymmetry: the SAME text is a valid caption on Facebook.
func TestCaps_CaptionAsTitleAsymmetry(t *testing.T) {
	caption := strings.Repeat("b", 214)
	if vs := validateCaps(Cell{Platform: "facebook", Title: caption}); len(vs) != 0 {
		t.Errorf("facebook caption of 214 chars should pass, got %+v", vs)
	}
	if vs := validateCaps(Cell{Platform: "youtube", Title: caption, Description: caption}); len(vs) == 0 {
		t.Errorf("youtube title of 214 chars should fail")
	}
}

func TestCaps_YouTubeMissingTitleFails(t *testing.T) {
	vs := validateCaps(Cell{Platform: "youtube", Title: "", Description: "the whole caption"})
	if !hasVerdict(vs, "youtube", "title", "required") {
		t.Fatalf("expected required-title verdict, got %+v", vs)
	}
}

func TestCaps_UnknownPlatformFailsClosed(t *testing.T) {
	vs := validateCaps(Cell{Label: "MASTODON some-acct", Title: "hi"})
	if !hasVerdict(vs, "unknown", "platform", "no constraints row") {
		t.Fatalf("expected fail-closed verdict, got %+v", vs)
	}
}

func TestCaps_RuneNotByteCount(t *testing.T) {
	title := strings.Repeat("—", 100) // 100 runes, 300 bytes
	if vs := validateCaps(Cell{Platform: "youtube", Title: title}); len(vs) != 0 {
		t.Errorf("100 em-dashes must fit the 100-rune cap, got %+v", vs)
	}
}

func TestCaps_HashtagCountAndBudget(t *testing.T) {
	many := make([]string, 31)
	for i := range many {
		many[i] = "tag"
	}
	vs := validateCaps(Cell{Platform: "instagram", Title: "ok", Hashtags: many})
	if !hasVerdict(vs, "instagram", "hashtags", "count 31 exceeds") {
		t.Fatalf("expected hashtag count verdict, got %+v", vs)
	}
	vs = validateCaps(Cell{Platform: "threads", Title: strings.Repeat("c", 495), Hashtags: []string{"longtag"}})
	if !hasVerdict(vs, "threads", "hashtags", "plus hashtags length") {
		t.Fatalf("expected caption+hashtags budget verdict, got %+v", vs)
	}
	vs = validateCaps(Cell{Platform: "threads", Title: "fine", Hashtags: []string{"bad tag"}})
	if !hasVerdict(vs, "threads", "hashtags", "malformed") {
		t.Fatalf("expected malformed hashtag verdict, got %+v", vs)
	}
}

func TestDuration_DefaultsAndOverride(t *testing.T) {
	p := &model.Profile{}
	vs := validateDuration(Cell{ClipID: "c", Kind: KindClip, Platform: "threads", DurationSec: 301}, MaxDurations(p))
	if !hasVerdict(vs, "threads", "duration_sec", "exceeds threads max 300s") {
		t.Fatalf("expected threads duration verdict, got %+v", vs)
	}
	if vs := validateDuration(Cell{Kind: KindStatic, Platform: "threads", DurationSec: 9999}, MaxDurations(p)); len(vs) != 0 {
		t.Errorf("statics carry no duration check, got %+v", vs)
	}
	p.Extra = map[string]any{"max_duration_sec": map[string]any{"threads": float64(600), "tiktok": "1200"}}
	md := MaxDurations(p)
	if md["threads"] != 600 || md["tiktok"] != 1200 || md["youtube"] != 43200 {
		t.Fatalf("override not applied: %+v", md)
	}
}

func TestCensor_WholeWordCaseInsensitive(t *testing.T) {
	cz := NewCensor([]string{"Damn", "son of a gun"})
	cases := map[string]string{
		"DAMN it":               "damn",
		"what a #damn thing":    "damn",
		"damnation is fine":     "",
		"you son of a gun, you": "son of a gun",
		"clean":                 "",
	}
	for in, want := range cases {
		if got := cz.Match(in); got != want {
			t.Errorf("Match(%q) = %q, want %q", in, got, want)
		}
	}
	vs := validateCensor(Cell{Platform: "youtube", Title: "ok", Description: "fine", Hashtags: []string{"#Damn"}}, cz)
	if !hasVerdict(vs, "youtube", "hashtags", "listed word") {
		t.Fatalf("expected hashtag censor verdict, got %+v", vs)
	}
}

func TestLoadCensorList_Shapes(t *testing.T) {
	dir := t.TempDir()
	arr := filepath.Join(dir, "a.json")
	os.WriteFile(arr, []byte(`["one","Two"]`), 0o644)
	obj := filepath.Join(dir, "o.json")
	os.WriteFile(obj, []byte(`{"strong":["three"],"note":"x","more":["four","one"]}`), 0o644)
	w, err := LoadCensorList(arr)
	if err != nil || len(w) != 2 || w[1] != "two" {
		t.Fatalf("array shape: %v %v", w, err)
	}
	w, err = LoadCensorList(obj)
	if err != nil || len(w) != 3 {
		t.Fatalf("object shape: %v %v", w, err)
	}
}

func TestTiming_MissingAndPast(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	if vs := validateTiming(Cell{Platform: "youtube"}, now); !hasVerdict(vs, "youtube", "publish_at", "empty") {
		t.Errorf("empty publish_at must fail, got %+v", vs)
	}
	if vs := validateTiming(Cell{Platform: "youtube", PublishAt: "2026-10-21T11:00:00Z"}, now); !hasVerdict(vs, "youtube", "publish_at", "not after now") {
		t.Errorf("past publish_at must fail, got %+v", vs)
	}
	if vs := validateTiming(Cell{Platform: "youtube", Status: StatusHeld}, now); len(vs) != 0 {
		t.Errorf("held cells skip timing, got %+v", vs)
	}
	if vs := validateTiming(Cell{Platform: "youtube", PublishAt: "2026-10-22T11:00:00Z"}, now); len(vs) != 0 {
		t.Errorf("future publish_at passes, got %+v", vs)
	}
	// Statics go into the Buffer queue: no publish time needed, but one they
	// carry must still be valid. OpusClip cells still need one.
	if vs := validateTiming(Cell{Kind: KindStatic, Platform: "instagram"}, now); len(vs) != 0 {
		t.Errorf("a static without publish_at passes, got %+v", vs)
	}
	if vs := validateTiming(Cell{Kind: KindStatic, Platform: "instagram", PublishAt: "2026-10-21T11:00:00Z"}, now); !hasVerdict(vs, "instagram", "publish_at", "not after now") {
		t.Errorf("a static with a past publish_at must fail, got %+v", vs)
	}
	for _, kind := range []string{KindClip, KindFull} {
		if vs := validateTiming(Cell{Kind: kind, Platform: "youtube"}, now); !hasVerdict(vs, "youtube", "publish_at", "empty") {
			t.Errorf("a %s cell without publish_at must fail, got %+v", kind, vs)
		}
	}
}

// TestStatic_NoPublishAtPassesWholeGate: a static with no publish_at runs
// through every check (cadence included, which counts clip and full cells
// only) without a verdict.
func TestStatic_NoPublishAtPassesWholeGate(t *testing.T) {
	p := testProfile()
	cells := []Cell{
		{Kind: KindStatic, ClipID: "s1", Platform: "instagram", AccountID: "buf-ig", Title: "A quote", Description: "A quote", Label: "static s1 (quote)"},
		{Kind: KindStatic, ClipID: "s2", Platform: "instagram", AccountID: "buf-ig", Title: "Another", Description: "Another", Label: "static s2 (quote)"},
	}
	vs, err := Validate(p, cells, nil, Options{Now: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)})
	if err != nil || len(vs) != 0 {
		t.Fatalf("statics without publish_at: %v %+v", err, vs)
	}
}

func testProfile() *model.Profile {
	return &model.Profile{
		Name: "t", Inbox: "/tmp", Orientation: "portrait", Timezone: "America/Los_Angeles",
		SlotsLocal: []string{"09:00", "12:00", "15:00", "18:00", "21:00"}, BacklogDays: 25,
		Cadence: map[string]int{"youtube": 2, "instagram": 1, "linkedin": 1, "tiktok": 2, "facebook": 1},
		Censor:  model.CensorConfig{Enabled: true, Mode: "beep"},
		// Probe: no TikTok or X account connected; facebook and linkedin
		// share one postAccountId across sub-accounts.
		OpusClip: model.OpusClipConfig{Shorts: model.CurationConfig{Model: "ClipBasic"}, Accounts: map[string]string{
			"youtube": "yt-acc", "instagram": "ig-acc", "facebook": "fb-acc", "linkedin_page": "li-acc", "linkedin_profile": "li-acc", "threads": "th-acc",
		}},
		Buffer: model.BufferConfig{Channels: map[string]string{"youtube": "buf-yt", "instagram": "buf-ig"}},
	}
}

// Probe (3): a skipCurate full video has null title metadata, so every
// platform row must require a title.
func TestConstraints_TitleRequiredEverywhere(t *testing.T) {
	for plat, pc := range Constraints {
		if !pc.Fields["title"].Required {
			t.Errorf("%s: title must be required", plat)
		}
	}
	vs := validateCaps(Cell{Kind: KindFull, Platform: "facebook", Title: "", Description: "x"})
	if !hasVerdict(vs, "facebook", "title", "required") {
		t.Errorf("empty full-video title must fail, got %+v", vs)
	}
}

// Probe (1) and (2): unconnected platforms and missing sub-accounts.
func TestAccount_UnconnectedAndSubAccount(t *testing.T) {
	p := testProfile()
	vs := validateAccount(p, Cell{ClipID: "c", Kind: KindClip, Platform: "tiktok", AccountID: "whatever"})
	if !hasVerdict(vs, "tiktok", "account_id", "no account for tiktok") || vs[0].FixHint != "connect the account in OpusClip and add it to the profile" {
		t.Fatalf("expected unconnected verdict with the fix hint, got %+v", vs)
	}
	if vs := validateAccount(p, Cell{Kind: KindClip, Platform: "youtube"}); !hasVerdict(vs, "youtube", "account_id", "empty") {
		t.Errorf("empty account_id must fail, got %+v", vs)
	}
	if vs := validateAccount(p, Cell{Kind: KindClip, Platform: "youtube", AccountID: "other"}); !hasVerdict(vs, "youtube", "account_id", "not the profile") {
		t.Errorf("mismatched account_id must fail, got %+v", vs)
	}
	if vs := validateAccount(p, Cell{Kind: KindClip, Platform: "facebook", AccountID: "fb-acc"}); !hasVerdict(vs, "facebook", "sub_account_id", "sub_account_id is empty") {
		t.Errorf("facebook without sub_account_id must fail, got %+v", vs)
	}
	if vs := validateAccount(p, Cell{Kind: KindClip, Platform: "facebook", AccountID: "fb-acc", SubAccountID: "page1"}); len(vs) != 0 {
		t.Errorf("facebook with sub_account_id passes, got %+v", vs)
	}
	if vs := validateAccount(p, Cell{Kind: KindStatic, Platform: "instagram", AccountID: "buf-ig"}); len(vs) != 0 {
		t.Errorf("static on a Buffer channel passes, got %+v", vs)
	}
	if vs := validateAccount(p, Cell{Kind: KindStatic, Platform: "threads", AccountID: "x"}); !hasVerdict(vs, "threads", "account_id", "no account for threads static") {
		t.Errorf("static with no Buffer channel must fail, got %+v", vs)
	}
}

// TestAccount_StaticBufferChannelExactThenFamily: a static's Buffer channel
// resolves like parse-decision resolves it: the exact platform key, else the
// family key (linkedin for linkedin_page and linkedin_profile).
func TestAccount_StaticBufferChannelExactThenFamily(t *testing.T) {
	p := testProfile()
	p.Buffer.Channels = map[string]string{"linkedin": "buf-li", "facebook_page": "buf-fbp"}
	for _, plat := range []string{"linkedin_page", "linkedin_profile"} {
		if vs := validateAccount(p, Cell{Kind: KindStatic, Platform: plat, AccountID: "buf-li"}); len(vs) != 0 {
			t.Errorf("%s static on the family channel must pass, got %+v", plat, vs)
		}
	}
	if vs := validateAccount(p, Cell{Kind: KindStatic, Platform: "linkedin_page", AccountID: "other"}); !hasVerdict(vs, "linkedin_page", "account_id", "not the profile") {
		t.Errorf("a static on another channel must fail, got %+v", vs)
	}
	// The exact key wins over the family.
	p.Buffer.Channels["linkedin_page"] = "buf-li-page"
	if vs := validateAccount(p, Cell{Kind: KindStatic, Platform: "linkedin_page", AccountID: "buf-li-page"}); len(vs) != 0 {
		t.Errorf("exact key must win, got %+v", vs)
	}
	if vs := validateAccount(p, Cell{Kind: KindStatic, Platform: "linkedin_page", AccountID: "buf-li"}); !hasVerdict(vs, "linkedin_page", "account_id", "not the profile") {
		t.Errorf("the family channel must not pass once the exact key is set, got %+v", vs)
	}
	// Clips never read Buffer channels.
	if vs := validateAccount(p, Cell{Kind: KindClip, Platform: "linkedin_page", AccountID: "buf-li", SubAccountID: "s"}); !hasVerdict(vs, "linkedin_page", "account_id", "not the profile") {
		t.Errorf("a clip must use the OpusClip account, got %+v", vs)
	}
}

func TestCadence_CountsExistingAcrossLocalDay(t *testing.T) {
	p := testProfile()
	loc, _ := time.LoadLocation(p.Timezone)
	// 2026-10-22 local: two existing youtube posts already (cadence 2).
	existing := []Cell{
		{ClipID: "e1", Kind: KindClip, Platform: "youtube", PublishAt: "2026-10-22T16:00:00Z"},
		{ClipID: "e2", Kind: KindClip, Platform: "youtube", PublishAt: "2026-10-22T19:00:00Z"},
	}
	cells := []Cell{
		{ClipID: "n1", Kind: KindClip, Platform: "youtube", PublishAt: "2026-10-23T04:00:00Z"}, // 21:00 local 10-22
		{ClipID: "n2", Kind: KindClip, Platform: "youtube", PublishAt: "2026-10-23T16:00:00Z"}, // 10-23 local, fine
		{ClipID: "s1", Kind: KindStatic, Platform: "youtube", PublishAt: "2026-10-23T01:00:00Z"},
		{ClipID: "h1", Kind: KindClip, Platform: "youtube", Status: StatusHeld},
	}
	vs := validateCadence(p, cells, existing, loc)
	if len(vs) != 1 || vs[0].ClipID != "n1" || !strings.Contains(vs[0].Reason, "3 youtube posts on 2026-10-22 exceed cadence 2") {
		t.Fatalf("want one cadence verdict for n1, got %+v", vs)
	}
	// linkedin_page falls back to the linkedin family cadence.
	if CadenceFor(p, "linkedin_page") != 1 || CadenceFor(p, "threads") != 0 {
		t.Fatalf("CadenceFor fallback wrong")
	}
	// Probe (2): cadence is per (platform, sub_account_id). Two Facebook
	// pages each get 1/day; two posts on one page the same day do not.
	pages := []Cell{
		{ClipID: "p1", Kind: KindClip, Platform: "facebook", SubAccountID: "page1", PublishAt: "2026-10-22T16:00:00Z"},
		{ClipID: "p2", Kind: KindClip, Platform: "facebook", SubAccountID: "page2", PublishAt: "2026-10-22T16:00:00Z"},
	}
	if vs := validateCadence(p, pages, nil, loc); len(vs) != 0 {
		t.Errorf("two pages on one day must pass, got %+v", vs)
	}
	pages[1].SubAccountID = "page1"
	if vs := validateCadence(p, pages, nil, loc); len(vs) != 2 || !strings.Contains(vs[0].Reason, "facebook/page1") {
		t.Errorf("same page twice must fail per sub-account, got %+v", vs)
	}
}

// End to end through Main: a profile on disk, a mixed batch, exit 65 with
// one JSON verdict per line in the documented shape, and exit 0 when clean.
func TestMain_Exit65VerdictShapeAndExit0(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LONGFORM_CONFIG_DIR", cfg)
	os.MkdirAll(filepath.Join(cfg, "profiles"), 0o755)
	pb, _ := json.Marshal(testProfile())
	os.WriteFile(filepath.Join(cfg, "profiles", "t.json"), pb, 0o644)

	dir := t.TempDir()
	censor := filepath.Join(dir, "censor.json")
	os.WriteFile(censor, []byte(`["heck"]`), 0o644)
	bad := []Cell{
		{ClipID: "a", Kind: KindClip, Platform: "youtube", AccountID: "yt-acc", Label: "YOUTUBE EVC", Title: strings.Repeat("t", 101), Description: "d", DurationSec: 30, PublishAt: "2027-01-02T17:00:00Z"},
		{ClipID: "b", Kind: KindClip, Platform: "threads", AccountID: "th-acc", Title: "what the heck", DurationSec: 400, PublishAt: "2027-01-02T17:00:00Z"},
		{ClipID: "c", Kind: KindFull, Platform: "facebook", AccountID: "fb-acc", Title: "fine"},
		{ClipID: "d", Kind: KindClip, Platform: "tiktok", AccountID: "tt", Title: "fine", PublishAt: "2027-01-02T17:00:00Z"},
	}
	cellsPath := filepath.Join(dir, "cells.json")
	var buf bytes.Buffer
	WriteCells(&buf, bad)
	os.WriteFile(cellsPath, buf.Bytes(), 0o644)

	var stdout, stderr bytes.Buffer
	code := Main([]string{"validate", "--profile", "t", "--cells", cellsPath, "--censor-list", censor, "--now", "2026-10-21T00:00:00Z"}, nil, &stdout, &stderr)
	if code != 65 {
		t.Fatalf("exit %d, want 65; stderr=%s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	var got []Verdict
	for _, l := range lines {
		var v Verdict
		if err := json.Unmarshal([]byte(l), &v); err != nil {
			t.Fatalf("verdict line %q not JSON: %v", l, err)
		}
		var keys map[string]any
		json.Unmarshal([]byte(l), &keys)
		for _, k := range []string{"clip_id", "platform", "field", "reason"} {
			if _, ok := keys[k]; !ok {
				t.Errorf("verdict missing %q: %s", k, l)
			}
		}
		got = append(got, v)
	}
	want := []struct{ clip, field, reason string }{
		{"a", "title", "exceeds youtube cap 100"},
		{"b", "title", "listed word"},
		{"b", "duration_sec", "exceeds threads max 300s"},
		{"c", "publish_at", "empty"},
		{"c", "sub_account_id", "sub_account_id is empty"},
		{"d", "account_id", "no account for tiktok"},
	}
	for _, w := range want {
		found := false
		for _, v := range got {
			if v.ClipID == w.clip && v.Field == w.field && strings.Contains(v.Reason, w.reason) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing verdict %+v in %s", w, stdout.String())
		}
	}

	good := []Cell{{ClipID: "a", Kind: KindClip, Platform: "youtube", AccountID: "yt-acc", Title: "short", Description: "d", DurationSec: 30, PublishAt: "2027-01-02T17:00:00Z", Hashtags: []string{"#ok"}}}
	buf.Reset()
	WriteCells(&buf, good)
	os.WriteFile(cellsPath, buf.Bytes(), 0o644)
	stdout.Reset()
	stderr.Reset()
	if code := Main([]string{"validate", "--profile", "t", "--cells", cellsPath, "--censor-list", censor, "--now", "2026-10-21T00:00:00Z"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("clean run must print nothing on stdout, got %s", stdout.String())
	}
	if code := Main([]string{"validate"}, nil, &stdout, &stderr); code != 64 {
		t.Errorf("missing flags exit %d, want 64", code)
	}
}
