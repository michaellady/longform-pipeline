// Package fakeopus is an in-memory stand-in for the OpusClip REST API
// (https://api.opus.pro/api) plus the Google Cloud Storage resumable-upload
// endpoints the real 4-step upload goes through. It exists so the pipeline
// can be driven end to end with no credits, no network, and assertable
// traffic: the e2e test in internal/e2e and the setup wizard's dry run
// (`longform fakeopus`) both use it.
//
// Fidelity notes (mirrors references/api-reference.md and the jq paths in the
// opusclip CLI script):
//
//   - POST /api/upload-links            -> {url, uploadId, dnsUrl, useAmount, totalAmount}
//   - POST <url> (x-goog-resumable)     -> 201 + Location header
//   - PUT  <Location>                   -> 200; the body is parsed as MP4 to learn its duration
//   - POST /api/clip-projects           -> {projectId}
//   - GET  /api/exportable-clips?q=findByProjectId&projectId=P
//     -> {data: []} until ProcessingDelay has elapsed, then {data: [clip...]}
//     where every clip has id "P.C", curationId "C", score, title,
//     description, hashtags, durationMs, isBonusClip, uriForPreview,
//     uriForExport, uriForThumbnail, judgeResult, text, renderPref.
//   - GET  /api/exportable-clips/P.C?include=editingScript -> clip + editingScript
//   - POST /api/exportable-clips/P.C/re-render -> {jobId}
//   - POST /api/publish-schedules       -> 201 {data: {scheduleId}}
//   - DELETE /api/publish-schedules/ID  -> {data: ID}
//   - GET  /api/social-accounts?q=mine  -> {data: [...]}
//   - GET  /api/brand-templates?q=mine  -> {data: [...]}
//
// A shorts project (curationPref without skipCurate) yields three clips
// (28 s / 55 s / 88 s, scores 91 / 76 / 64, the last one a bonus clip). A
// full project (curationPref.skipCurate true) yields one clip whose duration
// is, in priority order: a "<n>s" token in uploadedVideoAttr.title, the
// mvhd duration of the uploaded MP4, or Options.DefaultDurationSec.
//
// Every request to /api/ must carry "Authorization: Bearer <Options.APIKey>"
// or it gets a 401; the request log records whether each request matched,
// never the value itself.
package fakeopus

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Options configures a Server. The zero value is usable.
type Options struct {
	// APIKey is the bearer token every /api request must present. Default "test".
	APIKey string
	// ProcessingDelay is how long after project creation clips become
	// exportable (and how long a re-render stays pending). Default 0.
	ProcessingDelay time.Duration
	// DefaultDurationSec is the full-project clip duration when neither the
	// project title nor the uploaded MP4 declares one. Default 300.
	DefaultDurationSec int
	// Logf, when set, receives one line per request (method, path, status).
	Logf func(format string, args ...any)
}

// Request is one logged HTTP request. Authorization is never stored; see
// HasAuth and AuthMatched.
type Request struct {
	Seq         int               `json:"seq"`
	At          time.Time         `json:"at"`
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	Query       string            `json:"query,omitempty"`
	Headers     map[string]string `json:"headers"`
	Body        string            `json:"body,omitempty"`
	BodyBytes   int               `json:"body_bytes"`
	HasAuth     bool              `json:"has_auth"`
	AuthMatched bool              `json:"auth_matched"`
	Status      int               `json:"status"`
}

// Clip is the server's record of one exportable clip.
type Clip struct {
	ProjectID   string
	ClipID      string
	Title       string
	Description string
	Hashtags    string
	Score       float64
	DurationMs  int64
	IsBonus     bool
	Rank        int
	rerenderAt  time.Time // zero = never re-rendered
}

// Project is the server's record of one clip project.
type Project struct {
	ID         string
	Title      string
	UploadID   string
	SkipCurate bool
	Request    map[string]any
	CreatedAt  time.Time
	Clips      []Clip
}

type upload struct {
	id         string
	bytes      int
	durationMs int64
	done       bool
}

// Server is the fake. Create with New, serve with Start or Handler.
type Server struct {
	opts Options
	mux  *http.ServeMux

	mu        sync.Mutex
	base      string
	ln        net.Listener
	httpSrv   *http.Server
	requests  []Request
	uploads   map[string]*upload
	projects  map[string]*Project
	schedules map[string]map[string]any
	seq       int
	nextID    int

	inject429 atomic.Int32
}

// New builds a Server. Call Start to listen or Handler to mount it yourself.
func New(opts Options) *Server {
	if opts.APIKey == "" {
		opts.APIKey = "test"
	}
	if opts.DefaultDurationSec == 0 {
		opts.DefaultDurationSec = 300
	}
	s := &Server{
		opts:      opts,
		mux:       http.NewServeMux(),
		uploads:   map[string]*upload{},
		projects:  map[string]*Project{},
		schedules: map[string]map[string]any{},
	}
	s.routes()
	return s
}

// Start listens on addr ("127.0.0.1:0" for an ephemeral port) and returns
// the API base URL, e.g. http://127.0.0.1:54321/api, which is what
// OPUSCLIP_API_URL should be set to.
func (s *Server) Start(addr string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.ln = ln
	s.base = "http://" + ln.Addr().String()
	s.httpSrv = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	s.mu.Unlock()
	go func() { _ = s.httpSrv.Serve(ln) }()
	return s.APIURL(), nil
}

// SetBase records the externally visible base URL (scheme://host:port) when
// the handler is mounted by someone else, e.g. httptest.NewServer.
func (s *Server) SetBase(base string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.base = strings.TrimSuffix(base, "/")
}

// APIURL returns <base>/api.
func (s *Server) APIURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.base + "/api"
}

// Close stops a server started with Start.
func (s *Server) Close() error {
	s.mu.Lock()
	srv := s.httpSrv
	s.mu.Unlock()
	if srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

// Handler returns the http.Handler (request logging and auth included).
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serve) }

// Inject429 makes the next n /api requests fail with 429 Too Many Requests
// (Retry-After: 1). Use it to prove the client backs off and retries.
func (s *Server) Inject429(n int) { s.inject429.Store(int32(n)) }

// Requests returns a copy of the request log.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.requests))
	copy(out, s.requests)
	return out
}

// RequestsMatching filters the log by method ("" = any) and path prefix.
func (s *Server) RequestsMatching(method, pathPrefix string) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if (method == "" || r.Method == method) && strings.HasPrefix(r.Path, pathPrefix) {
			out = append(out, r)
		}
	}
	return out
}

// UnexpectedAuth returns every /api request whose Authorization header was
// missing or did not equal "Bearer <APIKey>".
func (s *Server) UnexpectedAuth() []Request {
	var out []Request
	for _, r := range s.Requests() {
		if strings.HasPrefix(r.Path, "/api/") && !r.AuthMatched {
			out = append(out, r)
		}
	}
	return out
}

// Project returns a project by id.
func (s *Server) Project(id string) (Project, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.projects[id]
	if !ok {
		return Project{}, false
	}
	cp := *p
	cp.Clips = append([]Clip(nil), p.Clips...)
	return cp, true
}

// Projects returns every project, oldest first.
func (s *Server) Projects() []Project {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Project
	for _, p := range s.projects {
		out = append(out, *p)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CreatedAt.Before(out[j-1].CreatedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Schedules returns every publish schedule by scheduleId (verbatim bodies).
func (s *Server) Schedules() map[string]map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]map[string]any, len(s.schedules))
	for k, v := range s.schedules {
		out[k] = v
	}
	return out
}

// ---------------------------------------------------------------------------
// routing

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("POST /api/upload-links", s.handleUploadLink)
	m.HandleFunc("POST /gcs/upload/{id}", s.handleResumableStart)
	m.HandleFunc("PUT /gcs/session/{id}", s.handleResumablePut)
	m.HandleFunc("POST /api/clip-projects", s.handleCreateProject)
	m.HandleFunc("GET /api/clip-projects/{id}", s.handleGetProject)
	m.HandleFunc("POST /api/clip-projects/{id}/update-visibility", s.handleVisibility)
	m.HandleFunc("GET /api/exportable-clips", s.handleFindClips)
	m.HandleFunc("GET /api/exportable-clips/{full}", s.handleGetClip)
	m.HandleFunc("POST /api/exportable-clips/{full}/re-render", s.handleReRender)
	m.HandleFunc("POST /api/publish-schedules", s.handleSchedule)
	m.HandleFunc("DELETE /api/publish-schedules/{id}", s.handleCancelSchedule)
	m.HandleFunc("GET /api/social-accounts", s.handleSocialAccounts)
	m.HandleFunc("GET /api/brand-templates", s.handleBrandTemplates)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }

const maxLoggedBody = 64 << 10

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	sw := &statusWriter{ResponseWriter: w, status: 200}
	rec := Request{At: time.Now(), Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Headers: map[string]string{}}
	for k, v := range r.Header {
		val := strings.Join(v, ", ")
		if strings.EqualFold(k, "Authorization") {
			val = "<redacted>"
		}
		rec.Headers[k] = val
	}
	auth := r.Header.Get("Authorization")
	rec.HasAuth = auth != ""
	rec.AuthMatched = auth == "Bearer "+s.opts.APIKey

	body, _ := io.ReadAll(r.Body)
	rec.BodyBytes = len(body)
	if r.Method == http.MethodPut || len(body) > maxLoggedBody {
		rec.Body = fmt.Sprintf("<%d bytes>", len(body))
	} else {
		rec.Body = string(body)
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	r = r.WithContext(context.WithValue(r.Context(), bodyKey{}, body))

	defer func() {
		rec.Status = sw.status
		s.mu.Lock()
		s.seq++
		rec.Seq = s.seq
		s.requests = append(s.requests, rec)
		s.mu.Unlock()
		if s.opts.Logf != nil {
			s.opts.Logf("fakeopus: %s %s%s -> %d", rec.Method, rec.Path, queryStr(rec.Query), rec.Status)
		}
	}()

	if strings.HasPrefix(r.URL.Path, "/api/") {
		if !rec.AuthMatched {
			writeJSON(sw, 401, map[string]any{"statusCode": 401, "message": "Unauthorized"})
			return
		}
		for {
			n := s.inject429.Load()
			if n <= 0 {
				break
			}
			if s.inject429.CompareAndSwap(n, n-1) {
				sw.Header().Set("Retry-After", "1")
				writeJSON(sw, 429, map[string]any{"statusCode": 429, "message": "Too Many Requests"})
				return
			}
		}
	}
	s.mux.ServeHTTP(sw, r)
}

type bodyKey struct{}

func queryStr(q string) string {
	if q == "" {
		return ""
	}
	return "?" + q
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) newID(prefix string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	return fmt.Sprintf("%s%08x%04d", prefix, time.Now().UnixNano()&0xffffffff, s.nextID)
}

func (s *Server) baseURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.base
}

// ---------------------------------------------------------------------------
// upload

func (s *Server) handleUploadLink(w http.ResponseWriter, r *http.Request) {
	id := s.newID("upload_")
	s.mu.Lock()
	s.uploads[id] = &upload{id: id}
	n := len(s.uploads)
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{
		"url":         s.baseURL() + "/gcs/upload/" + id,
		"uploadId":    id,
		"dnsUrl":      s.baseURL() + "/gcs/upload/" + id,
		"useAmount":   n,
		"totalAmount": 100,
	})
}

func (s *Server) handleResumableStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	_, ok := s.uploads[id]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, 404, map[string]any{"error": "unknown upload"})
		return
	}
	if !strings.EqualFold(r.Header.Get("x-goog-resumable"), "start") {
		writeJSON(w, 400, map[string]any{"error": "missing x-goog-resumable: start"})
		return
	}
	w.Header().Set("Location", s.baseURL()+"/gcs/session/"+id)
	w.WriteHeader(201)
}

func (s *Server) handleResumablePut(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	body, _ := r.Context().Value(bodyKey{}).([]byte)
	s.mu.Lock()
	u, ok := s.uploads[id]
	if ok {
		u.bytes = len(body)
		u.done = true
		if d, found := mp4DurationMs(body); found {
			u.durationMs = d
		}
	}
	s.mu.Unlock()
	if !ok {
		writeJSON(w, 404, map[string]any{"error": "unknown upload session"})
		return
	}
	writeJSON(w, 200, map[string]any{"kind": "storage#object", "name": id, "size": strconv.Itoa(len(body))})
}

// ---------------------------------------------------------------------------
// projects and clips

var titleDurationRe = regexp.MustCompile(`(?i)\b(\d+(?:\.\d+)?)\s*s(?:ec(?:onds?)?)?\b`)

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	body, _ := r.Context().Value(bodyKey{}).([]byte)
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, 400, map[string]any{"statusCode": 400, "message": "invalid JSON body"})
		return
	}
	videoURL, _ := req["videoUrl"].(string)
	if videoURL == "" {
		writeJSON(w, 400, map[string]any{"statusCode": 400, "message": "videoUrl is required"})
		return
	}
	title := ""
	if attr, ok := req["uploadedVideoAttr"].(map[string]any); ok {
		title, _ = attr["title"].(string)
	}
	skip := false
	if cp, ok := req["curationPref"].(map[string]any); ok {
		skip, _ = cp["skipCurate"].(bool)
	}
	var declaredMs int64
	if m := titleDurationRe.FindStringSubmatch(title); m != nil {
		if f, err := strconv.ParseFloat(m[1], 64); err == nil {
			declaredMs = int64(f * 1000)
		}
	}
	if declaredMs == 0 {
		s.mu.Lock()
		if u, ok := s.uploads[videoURL]; ok && u.durationMs > 0 {
			declaredMs = u.durationMs
		}
		s.mu.Unlock()
	}
	if declaredMs == 0 {
		declaredMs = int64(s.opts.DefaultDurationSec) * 1000
	}

	id := s.newID("proj_")
	p := &Project{ID: id, Title: title, UploadID: videoURL, SkipCurate: skip, Request: req, CreatedAt: time.Now()}
	if title == "" {
		p.Title = "Untitled " + id
	}
	if skip {
		p.Clips = []Clip{{ProjectID: id, ClipID: "clip_full_" + id[5:], Title: p.Title, Description: "Full-length render of " + p.Title, Hashtags: "#longform", Score: 0, DurationMs: declaredMs, Rank: 1}}
	} else {
		p.Clips = []Clip{
			{ProjectID: id, ClipID: "clip_a_" + id[5:], Title: "The one habit that fixed my mornings", Description: "A short hook about mornings.", Hashtags: "#morning #habits #evc", Score: 91, DurationMs: 28000, Rank: 1},
			{ProjectID: id, ClipID: "clip_b_" + id[5:], Title: "Why most advice about focus is wrong", Description: "Focus, reframed.", Hashtags: "#focus #productivity", Score: 76, DurationMs: 55000, Rank: 2},
			{ProjectID: id, ClipID: "clip_c_" + id[5:] + "_bonus", Title: "Bonus: the walk that started it", Description: "A bonus clip the curator almost cut.", Hashtags: "#bonus #walk", Score: 64, DurationMs: 88000, Rank: 3, IsBonus: true},
		}
	}
	s.mu.Lock()
	s.projects[id] = p
	s.mu.Unlock()
	writeJSON(w, 201, map[string]any{"projectId": id})
}

func (s *Server) projectReady(p *Project) bool {
	return time.Since(p.CreatedAt) >= s.opts.ProcessingDelay
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	p, ok := s.projects[r.PathValue("id")]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, 404, map[string]any{"statusCode": 404, "message": "project not found"})
		return
	}
	status := "PROCESSING"
	if s.projectReady(p) {
		status = "CONCLUDED"
	}
	writeJSON(w, 200, map[string]any{"projectId": p.ID, "title": p.Title, "status": status, "createdAt": p.CreatedAt.UTC().Format(time.RFC3339)})
}

func (s *Server) handleVisibility(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"projectId": r.PathValue("id"), "ok": true})
}

// clipJSON renders one clip in the real API's shape. The caller holds s.mu
// and passes base (s.base) so this never re-locks.
func (s *Server) clipJSON(base string, p *Project, c Clip) map[string]any {
	full := p.ID + "." + c.ClipID
	pending := !c.rerenderAt.IsZero() && time.Since(c.rerenderAt) < s.opts.ProcessingDelay
	aspect := "portrait"
	if rp, ok := p.Request["renderPref"].(map[string]any); ok {
		if a, ok := rp["layoutAspectRatio"].(string); ok && a != "" {
			aspect = a
		}
	}
	var score any = c.Score
	if p.SkipCurate {
		score = nil
	}
	return map[string]any{
		"id":              full,
		"curationId":      c.ClipID,
		"projectId":       p.ID,
		"rank":            c.Rank,
		"score":           score,
		"title":           c.Title,
		"description":     c.Description,
		"hashtags":        c.Hashtags,
		"durationMs":      c.DurationMs,
		"isBonusClip":     c.IsBonus,
		"uriForPreview":   base + "/cdn/preview/" + full + ".mp4",
		"uriForExport":    base + "/cdn/export/" + full + ".mp4",
		"uriForThumbnail": base + "/cdn/thumb/" + full + ".jpg",
		"text":            "This is the fake transcript for " + c.Title + ".",
		"clipKeywords":    strings.Fields(strings.ReplaceAll(c.Hashtags, "#", "")),
		"judgeResult": map[string]any{
			"hookScore": c.Score, "coherenceScore": c.Score - 2, "connectionScore": c.Score - 5, "trendScore": c.Score - 10,
		},
		"renderPref":           map[string]any{"layoutType": "AutoLayout", "layoutAspectRatio": aspect},
		"renderAsVideoPreview": map[string]any{"pending": pending},
		"renderAsVideoFile":    map[string]any{"pending": pending},
		"createdAt":            p.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) handleFindClips(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("q") != "findByProjectId" {
		writeJSON(w, 400, map[string]any{"statusCode": 400, "message": "unsupported q; fakeopus implements findByProjectId only"})
		return
	}
	s.mu.Lock()
	p, ok := s.projects[q.Get("projectId")]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, 404, map[string]any{"statusCode": 404, "message": "project not found"})
		return
	}
	data := []map[string]any{}
	if s.projectReady(p) {
		s.mu.Lock()
		for _, c := range p.Clips {
			data = append(data, s.clipJSON(s.base, p, c))
		}
		s.mu.Unlock()
	}
	writeJSON(w, 200, map[string]any{"data": data})
}

func (s *Server) findClip(full string) (*Project, *Clip, bool) {
	pid, cid, ok := strings.Cut(full, ".")
	if !ok {
		return nil, nil, false
	}
	p, ok := s.projects[pid]
	if !ok {
		return nil, nil, false
	}
	for i := range p.Clips {
		if p.Clips[i].ClipID == cid {
			return p, &p.Clips[i], true
		}
	}
	return nil, nil, false
}

func editingScript(c Clip) map[string]any {
	words := strings.Fields("This is the fake transcript for " + c.Title)
	var elems []map[string]any
	step := c.DurationMs / int64(len(words)+1)
	for i, wd := range words {
		elems = append(elems, map[string]any{"text": wd, "startMs": int64(i) * step, "endMs": int64(i+1) * step})
	}
	return map[string]any{
		"version":          "1",
		"sourceDurationMs": c.DurationMs,
		"sectionTimeline":  map[string]any{"in": 0, "out": c.DurationMs},
		"tracks": []map[string]any{
			{"trackType": "KeyFrameTrack", "sections": []any{}},
			{"trackType": "CaptionTrack", "sections": []map[string]any{{"segments": []map[string]any{{"content": map[string]any{"textElements": elems}}}}}},
			{"trackType": "EmojiTrack", "sections": []any{}},
			{"trackType": "TextOverlayTrack", "sections": []map[string]any{{"segments": []map[string]any{{"content": map[string]any{"text": c.Title}}}}}},
		},
	}
}

func (s *Server) handleGetClip(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	p, c, ok := s.findClip(r.PathValue("full"))
	var out map[string]any
	if ok {
		out = s.clipJSON(s.base, p, *c)
		if r.URL.Query().Get("include") == "editingScript" {
			out["editingScript"] = editingScript(*c)
		}
	}
	s.mu.Unlock()
	if !ok {
		writeJSON(w, 404, map[string]any{"statusCode": 404, "message": "clip not found"})
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleReRender(w http.ResponseWriter, r *http.Request) {
	body, _ := r.Context().Value(bodyKey{}).([]byte)
	var req struct {
		EditingScript map[string]any `json:"editingScript"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.EditingScript == nil {
		writeJSON(w, 400, map[string]any{"statusCode": 400, "message": "editingScript is required"})
		return
	}
	s.mu.Lock()
	_, c, ok := s.findClip(r.PathValue("full"))
	if ok {
		c.rerenderAt = time.Now()
	}
	s.mu.Unlock()
	if !ok {
		writeJSON(w, 404, map[string]any{"statusCode": 404, "message": "clip not found"})
		return
	}
	writeJSON(w, 200, map[string]any{"jobId": s.newID("rerender_")})
}

// ---------------------------------------------------------------------------
// social

func (s *Server) handleSchedule(w http.ResponseWriter, r *http.Request) {
	body, _ := r.Context().Value(bodyKey{}).([]byte)
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, 400, map[string]any{"statusCode": 400, "message": "invalid JSON body"})
		return
	}
	var missing []string
	for _, k := range []string{"projectId", "clipId", "postAccountId", "publishAt", "postDetail"} {
		if _, ok := req[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		writeJSON(w, 400, map[string]any{"statusCode": 400, "message": "missing fields: " + strings.Join(missing, ", ")})
		return
	}
	at, _ := req["publishAt"].(string)
	if _, err := time.Parse(time.RFC3339, at); err != nil {
		writeJSON(w, 400, map[string]any{"statusCode": 400, "message": "publishAt must be ISO 8601 UTC"})
		return
	}
	id := s.newID("sched_")
	s.mu.Lock()
	s.schedules[id] = req
	s.mu.Unlock()
	writeJSON(w, 201, map[string]any{"data": map[string]any{"scheduleId": id}})
}

func (s *Server) handleCancelSchedule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	_, ok := s.schedules[id]
	delete(s.schedules, id)
	s.mu.Unlock()
	if !ok {
		writeJSON(w, 404, map[string]any{"statusCode": 404, "message": "schedule not found"})
		return
	}
	writeJSON(w, 200, map[string]any{"data": id})
}

func (s *Server) handleSocialAccounts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"data": []map[string]any{
		{"postAccountId": "acc-yt", "subAccountId": "", "platform": "YOUTUBE", "extUserId": "UCfake", "extUserName": "Fake Channel", "extUserPictureLink": "", "extUserProfileLink": "https://youtube.com/@fake"},
		{"postAccountId": "acc-ig", "subAccountId": "ig-sub-1", "platform": "INSTAGRAM_BUSINESS", "extUserId": "17841400000", "extUserName": "fake.ig", "extUserPictureLink": "", "extUserProfileLink": "https://instagram.com/fake.ig"},
		{"postAccountId": "acc-li", "subAccountId": "li-sub-1", "platform": "LINKEDIN", "extUserId": "li-1", "extUserName": "Fake Person", "extUserPictureLink": "", "extUserProfileLink": "https://linkedin.com/in/fake"},
		{"postAccountId": "acc-tt", "subAccountId": "", "platform": "TIKTOK_BUSINESS", "extUserId": "tt-1", "extUserName": "fake.tt", "extUserPictureLink": "", "extUserProfileLink": "https://tiktok.com/@fake"},
	}})
}

func (s *Server) handleBrandTemplates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"data": []map[string]any{
		{"brandTemplateId": "tmpl_default", "name": "Default", "isDefault": true},
		{"brandTemplateId": "tmpl_evc", "name": "EVC", "isDefault": false},
	}})
}

// ---------------------------------------------------------------------------
// MP4 duration (mvhd) so the full clip's length tracks the uploaded file.

var errNoMoov = errors.New("no moov/mvhd box")

// mp4DurationMs walks the top-level boxes of an ISO BMFF file, finds moov,
// then mvhd, and returns duration/timescale in milliseconds.
func mp4DurationMs(b []byte) (int64, bool) {
	d, err := walkMP4(b)
	return d, err == nil && d > 0
}

func walkMP4(b []byte) (int64, error) {
	for off := int64(0); off+8 <= int64(len(b)); {
		size := int64(binary.BigEndian.Uint32(b[off : off+4]))
		typ := string(b[off+4 : off+8])
		hdr := int64(8)
		if size == 1 {
			if off+16 > int64(len(b)) {
				return 0, errNoMoov
			}
			size = int64(binary.BigEndian.Uint64(b[off+8 : off+16]))
			hdr = 16
		} else if size == 0 {
			size = int64(len(b)) - off
		}
		if size < hdr || off+size > int64(len(b)) {
			return 0, errNoMoov
		}
		if typ == "moov" {
			return mvhdDuration(b[off+hdr : off+size])
		}
		off += size
	}
	return 0, errNoMoov
}

func mvhdDuration(moov []byte) (int64, error) {
	for off := int64(0); off+8 <= int64(len(moov)); {
		size := int64(binary.BigEndian.Uint32(moov[off : off+4]))
		typ := string(moov[off+4 : off+8])
		if size < 8 || off+size > int64(len(moov)) {
			return 0, errNoMoov
		}
		if typ == "mvhd" {
			p := moov[off+8 : off+size]
			if len(p) < 4 {
				return 0, errNoMoov
			}
			version := p[0]
			var timescale, duration int64
			if version == 1 {
				if len(p) < 4+8+8+4+8 {
					return 0, errNoMoov
				}
				timescale = int64(binary.BigEndian.Uint32(p[20:24]))
				duration = int64(binary.BigEndian.Uint64(p[24:32]))
			} else {
				if len(p) < 4+4+4+4+4 {
					return 0, errNoMoov
				}
				timescale = int64(binary.BigEndian.Uint32(p[12:16]))
				duration = int64(binary.BigEndian.Uint32(p[16:20]))
			}
			if timescale == 0 {
				return 0, errNoMoov
			}
			return duration * 1000 / timescale, nil
		}
		off += size
	}
	return 0, errNoMoov
}
