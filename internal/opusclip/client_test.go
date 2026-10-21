package opusclip

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type recorder struct {
	mu   sync.Mutex
	reqs []recordedReq
}

type recordedReq struct {
	Method, Path string
	Header       http.Header
	Body         []byte
}

func (r *recorder) add(req *http.Request) recordedReq {
	b, _ := io.ReadAll(req.Body)
	rr := recordedReq{Method: req.Method, Path: req.URL.RequestURI(), Header: req.Header.Clone(), Body: b}
	r.mu.Lock()
	r.reqs = append(r.reqs, rr)
	r.mu.Unlock()
	return rr
}

func (r *recorder) find(method, pathPrefix string) []recordedReq {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []recordedReq
	for _, q := range r.reqs {
		if q.Method == method && strings.HasPrefix(q.Path, pathPrefix) {
			out = append(out, q)
		}
	}
	return out
}

func newTestClient(t *testing.T, srv *httptest.Server, opts ...Option) *Client {
	t.Helper()
	base := []Option{WithBaseURL(srv.URL), WithNoRateLimit(), WithRetry(2, time.Millisecond)}
	return New("test-key-DO-NOT-LOG", append(base, opts...)...)
}

// TestUploadFourSteps drives steps 1-3 against a fake API + fake GCS host and
// checks headers, streaming body, and that the key never reaches GCS.
func TestUploadFourSteps(t *testing.T) {
	rec := &recorder{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rr := rec.add(r)
		switch {
		case r.Method == "POST" && r.URL.Path == "/upload-links":
			if !strings.Contains(string(rr.Body), `"usecase":"LocalUpload"`) {
				t.Errorf("upload-links body = %s", rr.Body)
			}
			if r.Header.Get("Authorization") != "Bearer test-key-DO-NOT-LOG" {
				t.Errorf("missing bearer on API call")
			}
			fmt.Fprintf(w, `{"url":"%s/gcs/signed?sig=abc","uploadId":"up_123","dnsUrl":"x","useAmount":1,"totalAmount":100}`, srv.URL)
		case r.Method == "POST" && r.URL.Path == "/gcs/signed":
			if r.Header.Get("x-goog-resumable") != "start" {
				t.Errorf("resumable start header missing")
			}
			if r.Header.Get("Authorization") != "" {
				t.Errorf("API key leaked to storage host")
			}
			if r.ContentLength != 0 {
				t.Errorf("start session Content-Length = %d", r.ContentLength)
			}
			w.Header().Set("Location", srv.URL+"/gcs/session?upload_id=zzz")
			w.WriteHeader(201)
		case r.Method == "PUT" && r.URL.Path == "/gcs/session":
			if r.Header.Get("Content-Type") != "application/octet-stream" {
				t.Errorf("PUT content-type = %q", r.Header.Get("Content-Type"))
			}
			if r.Header.Get("Authorization") != "" {
				t.Errorf("API key leaked to storage host")
			}
			w.WriteHeader(200)
		case r.Method == "POST" && r.URL.Path == "/clip-projects":
			fmt.Fprint(w, `{"projectId":"proj_1","status":"QUEUED"}`)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, 404)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	file := filepath.Join(dir, "delivery.mp4")
	content := bytes.Repeat([]byte("video-bytes-"), 1000)
	if err := os.WriteFile(file, content, 0o644); err != nil {
		t.Fatal(err)
	}
	c := newTestClient(t, srv)
	ctx := context.Background()
	id, err := c.UploadFile(ctx, file)
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if id != "up_123" {
		t.Fatalf("uploadId = %q", id)
	}
	puts := rec.find("PUT", "/gcs/session")
	if len(puts) != 1 || !bytes.Equal(puts[0].Body, content) {
		t.Fatalf("expected one PUT with the file bytes, got %d", len(puts))
	}
	p, err := c.CreateProject(ctx, CreateProjectRequest{VideoURL: id, CurationPref: &CurationPref{SkipCurate: true}})
	if err != nil {
		t.Fatal(err)
	}
	if p.ProjectID != "proj_1" {
		t.Fatalf("projectId = %q", p.ProjectID)
	}
	cp := rec.find("POST", "/clip-projects")
	if len(cp) != 1 || !strings.Contains(string(cp[0].Body), `"videoUrl":"up_123"`) || !strings.Contains(string(cp[0].Body), `"skipCurate":true`) {
		t.Fatalf("clip-projects body = %s", cp[0].Body)
	}
}

// TestPutFileResumes simulates a dropped connection mid-PUT; the client must
// query the session with Content-Range bytes */N and send only the tail.
func TestPutFileResumes(t *testing.T) {
	content := bytes.Repeat([]byte("0123456789"), 500) // 5000 bytes
	var mu sync.Mutex
	var got []byte
	attempt := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != "PUT" {
			http.Error(w, "no", 404)
			return
		}
		cr := r.Header.Get("Content-Range")
		if r.ContentLength == 0 && strings.HasPrefix(cr, "bytes */") {
			// status query: we persisted the first 2000 bytes
			w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(got)-1))
			w.WriteHeader(308)
			return
		}
		attempt++
		if attempt == 1 {
			// take 2000 bytes then drop the connection
			buf := make([]byte, 2000)
			io.ReadFull(r.Body, buf)
			got = append(got, buf...)
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("no hijack")
			}
			conn, _, _ := hj.Hijack()
			conn.Close()
			return
		}
		if cr != fmt.Sprintf("bytes 2000-%d/%d", len(content)-1, len(content)) {
			t.Errorf("resume Content-Range = %q", cr)
		}
		b, _ := io.ReadAll(r.Body)
		got = append(got, b...)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	dir := t.TempDir()
	file := filepath.Join(dir, "f.bin")
	os.WriteFile(file, content, 0o644)
	c := newTestClient(t, srv, WithRetry(3, time.Millisecond))
	if err := c.PutFile(context.Background(), srv.URL+"/session", file); err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("server got %d bytes, want %d", len(got), len(content))
	}
	if attempt != 2 {
		t.Fatalf("attempts = %d", attempt)
	}
}

func TestStripOverlayBody(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		switch {
		case r.Method == "GET" && r.URL.Path == "/exportable-clips/P1.C1":
			if r.URL.Query().Get("include") != "editingScript" {
				t.Errorf("include = %q", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"id":"P1.C1","title":"t","renderPref":{"screenOverlays":[{"fileId":"logo"}]},"editingScript":{"version":3,"sectionTimeline":[{"a":1}]},"renderAsVideoFile":{"pending":false}}`)
		case r.Method == "POST" && r.URL.Path == "/exportable-clips/P1.C1/re-render":
			fmt.Fprint(w, `{"ok":true}`)
		default:
			http.Error(w, "unexpected "+r.URL.Path, 404)
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	raw, err := c.StripOverlay(context.Background(), "P1", "C1", false)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"ok":true}` {
		t.Fatalf("raw = %s", raw)
	}
	rr := rec.find("POST", "/exportable-clips/P1.C1/re-render")
	if len(rr) != 1 {
		t.Fatalf("re-render calls = %d", len(rr))
	}
	var body struct {
		EditingScript            json.RawMessage `json:"editingScript"`
		RenderPreferenceOverride map[string]any  `json:"renderPreferenceOverride"`
	}
	if err := json.Unmarshal(rr[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if string(body.EditingScript) != `{"version":3,"sectionTimeline":[{"a":1}]}` {
		t.Errorf("editingScript not echoed verbatim: %s", body.EditingScript)
	}
	o := body.RenderPreferenceOverride
	if v, ok := o["screenOverlays"].([]any); !ok || len(v) != 0 {
		t.Errorf("screenOverlays = %v", o["screenOverlays"])
	}
	for _, k := range []string{"screenOverlay", "screenOverlayFileId"} {
		if v, present := o[k]; !present || v != nil {
			t.Errorf("%s = %v (present=%v), want explicit null", k, v, present)
		}
	}
	if o["enableScreenOverlay"] != false || o["disableScreenOverlay"] != true {
		t.Errorf("overlay flags = %v", o)
	}
}

func TestStripOverlayNoScript(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"P1.C1","editingScript":null}`)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	_, err := c.StripOverlay(context.Background(), "P1", "C1", false)
	if err == nil || !strings.Contains(err.Error(), "no editingScript") {
		t.Fatalf("err = %v", err)
	}
}

// TestStripOverlaySkipsWhenNoOverlay mirrors the live template
// (screenOverlays: [], screenOverlay: null): no re-render is submitted
// unless force is set.
func TestStripOverlaySkipsWhenNoOverlay(t *testing.T) {
	reRenders := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/re-render") {
			reRenders++
			fmt.Fprint(w, `{"ok":true}`)
			return
		}
		fmt.Fprint(w, `{"id":"P1.C1","renderPref":{"screenOverlay":null,"screenOverlayFileId":null,"screenOverlays":[],"enableScreenOverlay":null,"disableScreenOverlay":false},"editingScript":{"v":1},"renderAsVideoFile":null}`)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	_, err := c.StripOverlay(context.Background(), "P1", "C1", false)
	if !errorsIs(err, ErrNoOverlay) || reRenders != 0 {
		t.Fatalf("err = %v, reRenders = %d", err, reRenders)
	}
	if _, err := c.StripOverlay(context.Background(), "P1", "C1", true); err != nil || reRenders != 1 {
		t.Fatalf("force: err = %v, reRenders = %d", err, reRenders)
	}
}

func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// TestLiveShapes decodes the field shapes the 2026-10-21 probes returned.
func TestLiveShapes(t *testing.T) {
	curated := `{"id":"P3102119Mduz.qp4hWnJTOl","rank":1,"score":87,"title":"T","description":"D","hashtags":"#Gastown #AIAgents #Code","durationMs":42067,"isBonusClip":false,"text":"a __silence b","uriForPreview":"u","uriForExport":"e","renderAsVideoFile":null,"renderAsVideoPreview":null}`
	cl, err := decodeClip(json.RawMessage(curated))
	if err != nil {
		t.Fatal(err)
	}
	if len(cl.Hashtags) != 3 || cl.Hashtags[0] != "#Gastown" || cl.ClipID() != "qp4hWnJTOl" || cl.DurationSec() != 42 {
		t.Fatalf("curated = %+v", cl)
	}
	if !cl.RenderReady() || cl.RenderPending() {
		t.Fatal("null renderAsVideoFile must count as ready")
	}
	if CleanTranscript(cl.Transcript()) != "a b" {
		t.Fatalf("clean = %q", CleanTranscript(cl.Transcript()))
	}
	full := `{"id":"P3102119MVDK.f6471d8959","rank":null,"score":null,"title":"probe_OpusClip Captions","description":null,"hashtags":null,"keywords":null,"durationMs":60000,"isBonusClip":false,"renderAsVideoFile":{"pending":true}}`
	cl, err = decodeClip(json.RawMessage(full))
	if err != nil {
		t.Fatal(err)
	}
	if cl.Hashtags != nil || cl.Score != 0 || cl.ClipID() != "f6471d8959" || cl.RenderReady() {
		t.Fatalf("full = %+v", cl)
	}
	var arr StringList
	if err := json.Unmarshal([]byte(`["x","y"]`), &arr); err != nil || len(arr) != 2 {
		t.Fatalf("array form: %v %v", arr, err)
	}
	// Create-project live response: id/projectId/stage/sourceInfo.
	p := `{"id":"P3102119MVDK","stage":"QUEUED","title":"probe","projectId":"P3102119MVDK","sourceInfo":{"durationMs":60000}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, p) }))
	defer srv.Close()
	proj, err := newTestClient(t, srv).CreateProject(context.Background(), CreateProjectRequest{VideoURL: "UPL_x"})
	if err != nil || proj.ProjectID != "P3102119MVDK" || proj.Stage != "QUEUED" || proj.SourceDurationMs != 60000 || proj.CreditEstimate() != 1 {
		t.Fatalf("project = %+v err=%v", proj, err)
	}
}

// TestSchedulePacing uses a fake clock: three schedule calls must be spaced
// one second apart (1 req/s) while the 30/min bucket never blocks.
func TestSchedulePacing(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if r.URL.Path != "/publish-schedules" {
			http.Error(w, "bad path", 404)
			return
		}
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"data":{"scheduleId":"s%d"}}`, n)
	}))
	defer srv.Close()
	now := time.Unix(1_700_000_000, 0)
	var slept []time.Duration
	fakeNow := func() time.Time { return now }
	fakeSleep := func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		now = now.Add(d)
		return nil
	}
	c := New("k", WithBaseURL(srv.URL), WithRetry(0, 0), withClock(fakeNow, fakeSleep))
	req := ScheduleRequest{ProjectID: "P", ClipID: "C", PostAccountID: "A", PublishAt: "2026-11-01T10:00:00Z", PostDetail: PostDetail{Title: "x"}}
	for i := 0; i < 3; i++ {
		res, err := c.Schedule(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if res.ScheduleID != fmt.Sprintf("s%d", i+1) {
			t.Fatalf("scheduleId = %q", res.ScheduleID)
		}
	}
	var total time.Duration
	for _, d := range slept {
		total += d
	}
	if len(slept) != 2 || total < 2*time.Second-time.Millisecond || total > 2*time.Second+time.Millisecond {
		t.Fatalf("expected two ~1s pacing sleeps, got %v", slept)
	}
}

func TestBucketLimits(t *testing.T) {
	now := time.Unix(0, 0)
	var slept time.Duration
	l := newLimiter(30, 2*time.Second)
	l.now = func() time.Time { return now }
	l.sleep = func(ctx context.Context, d time.Duration) error { slept += d; now = now.Add(d); return nil }
	for i := 0; i < 31; i++ {
		if err := l.wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if slept != 2*time.Second {
		t.Fatalf("31st call should wait one refill (2s), slept %v", slept)
	}
}

func TestRetryAndTypedErrors(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/flaky":
			if calls < 3 {
				w.WriteHeader(429)
				fmt.Fprint(w, `{"error":"slow down"}`)
				return
			}
			fmt.Fprint(w, `{"ok":1}`)
		case "/bad":
			w.WriteHeader(400)
			fmt.Fprint(w, `{"message":"nope"}`)
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv, WithRetry(3, time.Millisecond))
	if _, err := c.get(context.Background(), "/flaky"); err != nil {
		t.Fatalf("flaky should succeed after retries: %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d", calls)
	}
	calls = 0
	_, err := c.get(context.Background(), "/bad")
	var ae *APIError
	if !IsStatus(err, 400) || !errorsAs(err, &ae) || ae.Body != `{"message":"nope"}` || ae.Temporary() {
		t.Fatalf("err = %#v", err)
	}
	if calls != 1 {
		t.Fatalf("400 must not retry, calls = %d", calls)
	}
}

func errorsAs(err error, target **APIError) bool {
	for err != nil {
		if ae, ok := err.(*APIError); ok {
			*target = ae
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

type bufLogger struct{ sb strings.Builder }

func (b *bufLogger) Printf(f string, a ...any) { fmt.Fprintf(&b.sb, f+"\n", a...) }

func TestLoggerRedacts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) }))
	defer srv.Close()
	bl := &bufLogger{}
	c := newTestClient(t, srv, WithLogger(bl))
	c.get(context.Background(), "/x")
	c.Logger.Printf("dump: Authorization: Bearer %s and %s=%s", "test-key-DO-NOT-LOG", EnvAPIKey, "test-key-DO-NOT-LOG")
	out := bl.sb.String()
	if strings.Contains(out, "test-key-DO-NOT-LOG") {
		t.Fatalf("key leaked into log: %s", out)
	}
	if !strings.Contains(out, "GET /x -> 200") || !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("log = %s", out)
	}
}

func TestClipHelpers(t *testing.T) {
	cl := Clip{ID: "proj.abc.def", DurationMs: 61499}
	if cl.ProjectID() != "proj" || cl.ClipID() != "abc.def" || cl.DurationSec() != 61 {
		t.Fatalf("helpers: %q %q %d", cl.ProjectID(), cl.ClipID(), cl.DurationSec())
	}
	if d := Durations([]int{30, 60}); len(d) != 2 || d[0] != [2]int{0, 30} || d[1] != [2]int{0, 60} {
		t.Fatalf("Durations = %v", d)
	}
}
