package fakeopus

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func do(t *testing.T, method, url string, body []byte, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, b
}

func auth() map[string]string {
	return map[string]string{"Authorization": "Bearer test", "Content-Type": "application/json"}
}

// fakeMP4 builds a minimal ISO BMFF buffer with an mvhd declaring durSec.
func fakeMP4(durSec int) []byte {
	mvhd := make([]byte, 8+100)
	binary.BigEndian.PutUint32(mvhd[0:4], uint32(len(mvhd)))
	copy(mvhd[4:8], "mvhd")
	p := mvhd[8:]
	p[0] = 0                                                  // version 0
	binary.BigEndian.PutUint32(p[12:16], 1000)                // timescale
	binary.BigEndian.PutUint32(p[16:20], uint32(durSec*1000)) // duration
	moov := make([]byte, 8+len(mvhd))
	binary.BigEndian.PutUint32(moov[0:4], uint32(len(moov)))
	copy(moov[4:8], "moov")
	copy(moov[8:], mvhd)
	ftyp := []byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 1}
	return append(append(ftyp, moov...), []byte("mdat-ish padding")...)
}

func TestFullFlow(t *testing.T) {
	s := New(Options{ProcessingDelay: 150 * time.Millisecond})
	api, err := s.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !strings.HasSuffix(api, "/api") {
		t.Fatalf("API URL should end in /api, got %s", api)
	}

	// Unauthenticated and wrong-key requests are rejected and logged as such.
	resp, _ := do(t, "POST", api+"/upload-links", []byte(`{}`), nil)
	if resp.StatusCode != 401 {
		t.Fatalf("no auth: want 401, got %d", resp.StatusCode)
	}
	resp, _ = do(t, "POST", api+"/upload-links", []byte(`{}`), map[string]string{"Authorization": "Bearer wrong"})
	if resp.StatusCode != 401 {
		t.Fatalf("wrong key: want 401, got %d", resp.StatusCode)
	}
	if n := len(s.UnexpectedAuth()); n != 2 {
		t.Fatalf("UnexpectedAuth: want 2, got %d", n)
	}
	for _, r := range s.Requests() {
		if v, ok := r.Headers["Authorization"]; ok && v != "<redacted>" {
			t.Fatalf("Authorization leaked into log: %q", v)
		}
	}

	// Step 1-3: upload link, resumable start, PUT bytes.
	resp, b := do(t, "POST", api+"/upload-links", []byte(`{"video":{"usecase":"LocalUpload"}}`), auth())
	if resp.StatusCode != 200 {
		t.Fatalf("upload-links: %d %s", resp.StatusCode, b)
	}
	var ul struct {
		URL      string `json:"url"`
		UploadID string `json:"uploadId"`
	}
	if err := json.Unmarshal(b, &ul); err != nil || ul.URL == "" || ul.UploadID == "" {
		t.Fatalf("upload-links body: %s (%v)", b, err)
	}
	resp, _ = do(t, "POST", ul.URL, nil, map[string]string{"x-goog-resumable": "start", "Content-Length": "0"})
	loc := resp.Header.Get("Location")
	if resp.StatusCode != 201 || loc == "" {
		t.Fatalf("resumable start: %d Location=%q", resp.StatusCode, loc)
	}
	resp, _ = do(t, "PUT", loc, fakeMP4(7), map[string]string{"Content-Type": "application/octet-stream"})
	if resp.StatusCode != 200 {
		t.Fatalf("PUT bytes: %d", resp.StatusCode)
	}

	// Step 4: two projects.
	mk := func(body string) string {
		resp, b := do(t, "POST", api+"/clip-projects", []byte(body), auth())
		if resp.StatusCode != 201 {
			t.Fatalf("clip-projects: %d %s", resp.StatusCode, b)
		}
		var out struct {
			ProjectID string `json:"projectId"`
		}
		if err := json.Unmarshal(b, &out); err != nil || out.ProjectID == "" {
			t.Fatalf("clip-projects body: %s", b)
		}
		return out.ProjectID
	}
	shorts := mk(`{"videoUrl":"` + ul.UploadID + `","uploadedVideoAttr":{"title":"walk"},"curationPref":{"model":"ClipBasic","clipDurations":[[0,30],[0,60],[0,90]]},"renderPref":{"layoutAspectRatio":"portrait"}}`)
	full := mk(`{"videoUrl":"` + ul.UploadID + `","uploadedVideoAttr":{"title":"walk"},"curationPref":{"skipCurate":true},"renderPref":{"layoutAspectRatio":"portrait"}}`)

	clips := func(pid string) []map[string]any {
		resp, b := do(t, "GET", api+"/exportable-clips?q=findByProjectId&projectId="+pid, nil, auth())
		if resp.StatusCode != 200 {
			t.Fatalf("exportable-clips: %d %s", resp.StatusCode, b)
		}
		var out struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("exportable-clips body: %s", b)
		}
		return out.Data
	}
	if got := clips(shorts); len(got) != 0 {
		t.Fatalf("before delay: want 0 clips, got %d", len(got))
	}
	time.Sleep(200 * time.Millisecond)
	sc := clips(shorts)
	if len(sc) != 3 {
		t.Fatalf("shorts: want 3 clips, got %d", len(sc))
	}
	wantDur := []float64{28000, 55000, 88000}
	wantScore := []float64{91, 76, 64}
	bonus := 0
	for i, c := range sc {
		if c["durationMs"].(float64) != wantDur[i] || c["score"].(float64) != wantScore[i] {
			t.Fatalf("clip %d: %v", i, c)
		}
		if !strings.HasPrefix(c["id"].(string), shorts+".") {
			t.Fatalf("clip id should be <project>.<clip>: %v", c["id"])
		}
		if c["isBonusClip"].(bool) {
			bonus++
		}
	}
	if bonus != 1 {
		t.Fatalf("want exactly one bonus clip, got %d", bonus)
	}
	fc := clips(full)
	if len(fc) != 1 || fc[0]["durationMs"].(float64) != 7000 {
		t.Fatalf("full: want one 7000 ms clip (from the uploaded mvhd), got %v", fc)
	}

	// editingScript and re-render.
	fullID := fc[0]["id"].(string)
	resp, b = do(t, "GET", api+"/exportable-clips/"+fullID+"?include=editingScript", nil, auth())
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"editingScript"`) || !strings.Contains(string(b), "CaptionTrack") {
		t.Fatalf("get clip: %d %s", resp.StatusCode, b)
	}
	resp, b = do(t, "POST", api+"/exportable-clips/"+fullID+"/re-render", []byte(`{"editingScript":{"tracks":[]}}`), auth())
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"jobId"`) {
		t.Fatalf("re-render: %d %s", resp.StatusCode, b)
	}

	// Schedules.
	resp, b = do(t, "POST", api+"/publish-schedules", []byte(`{"projectId":"`+shorts+`","clipId":"x","postAccountId":"acc-yt","publishAt":"2030-01-01T10:00:00Z","postDetail":{"title":"t"}}`), auth())
	if resp.StatusCode != 201 {
		t.Fatalf("publish-schedules: %d %s", resp.StatusCode, b)
	}
	var sched struct {
		Data struct {
			ScheduleID string `json:"scheduleId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &sched); err != nil || sched.Data.ScheduleID == "" {
		t.Fatalf("publish-schedules body: %s", b)
	}
	resp, b = do(t, "POST", api+"/publish-schedules", []byte(`{"projectId":"p"}`), auth())
	if resp.StatusCode != 400 {
		t.Fatalf("incomplete schedule: want 400, got %d %s", resp.StatusCode, b)
	}
	resp, _ = do(t, "DELETE", api+"/publish-schedules/"+sched.Data.ScheduleID, nil, auth())
	if resp.StatusCode != 200 {
		t.Fatalf("cancel: %d", resp.StatusCode)
	}

	// 429 injection.
	s.Inject429(1)
	resp, _ = do(t, "GET", api+"/social-accounts?q=mine", nil, auth())
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("injected 429: got %d", resp.StatusCode)
	}
	resp, _ = do(t, "GET", api+"/social-accounts?q=mine", nil, auth())
	if resp.StatusCode != 200 {
		t.Fatalf("after 429: got %d", resp.StatusCode)
	}

	if n := len(s.RequestsMatching("POST", "/api/clip-projects")); n != 2 {
		t.Fatalf("want 2 clip-projects posts logged, got %d", n)
	}
}

func TestTitleDeclaresDuration(t *testing.T) {
	s := New(Options{DefaultDurationSec: 42})
	api, err := s.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tc := range []struct {
		title string
		want  float64
	}{
		{"evc walk (12s)", 12000},
		{"no hint here", 42000},
	} {
		resp, b := do(t, "POST", api+"/clip-projects", []byte(`{"videoUrl":"nope","uploadedVideoAttr":{"title":"`+tc.title+`"},"curationPref":{"skipCurate":true}}`), auth())
		if resp.StatusCode != 201 {
			t.Fatalf("%d %s", resp.StatusCode, b)
		}
		var out struct {
			ProjectID string `json:"projectId"`
		}
		_ = json.Unmarshal(b, &out)
		p, ok := s.Project(out.ProjectID)
		if !ok || len(p.Clips) != 1 || float64(p.Clips[0].DurationMs) != tc.want {
			t.Fatalf("title %q: want %v ms, got %+v", tc.title, tc.want, p.Clips)
		}
	}
}
