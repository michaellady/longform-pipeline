package opusclip

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestClipSchedulesLiveShape decodes the findByProjectAndClip response the
// 2026-10-25 probe returned and checks the query the client sends.
func TestClipSchedulesLiveShape(t *testing.T) {
	live := `{"data":[{"scheduleId":"1771352426007qJ8C-YOUTUBE","extUserId":"UCyxYcJWs5WstpM2h8KjabUw","extOrgId":null,"platform":"YOUTUBE","postDetail":{"title":"T","custom":{"description":"D","privacy":"public"},"thumbnailUrl":null},"publishAt":"2026-02-19T04:00:00.000Z"},{"scheduleId":"177135242600745pA-LINKEDIN","extUserId":"9mS0FNJDCZ","extOrgId":"urn:li:organization:109952978","platform":"LINKEDIN","postDetail":{"title":"T","custom":{"description":"D"},"thumbnailUrl":null},"publishAt":"2026-02-19T04:00:00.000Z"}]}`
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/publish-schedules" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		gotQuery = r.URL.RawQuery
		fmt.Fprint(w, live)
	}))
	defer srv.Close()
	c := newTestClient(t, srv)
	got, err := c.ClipSchedules(context.Background(), "P30122131P0V", "7wWDLGwoZM")
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "clipId=7wWDLGwoZM&projectId=P30122131P0V&q=findByProjectAndClip" {
		t.Errorf("query = %q", gotQuery)
	}
	if len(got) != 2 || got[0].ScheduleID != "1771352426007qJ8C-YOUTUBE" || got[0].Platform != "YOUTUBE" || got[0].PublishAt != "2026-02-19T04:00:00.000Z" || got[0].ExtOrgID != "" {
		t.Fatalf("decoded = %+v", got)
	}
	if got[1].ExtOrgID != "urn:li:organization:109952978" || len(got[1].Raw) == 0 {
		t.Fatalf("second = %+v raw=%s", got[1], got[1].Raw)
	}

	// An empty list (a clip never scheduled, or every post already out) is
	// not an error.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[]}`) }))
	defer srv2.Close()
	got, err = newTestClient(t, srv2).ClipSchedules(context.Background(), "P3102517uJJi", "c486ee767f")
	if err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	if _, err := c.ClipSchedules(context.Background(), "", "x"); err == nil {
		t.Fatal("empty project id must be refused")
	}
}

// TestClipSchedulesUnsupportedQ maps the API's 400 for an unknown q to a
// non-temporary APIError.
func TestClipSchedulesUnsupportedQ(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"errorMessage":"unsupported q parameter: 'x'. Supported values: byPage, findByProjectAndClip","errorName":"BadRequestError"}`)
	}))
	defer srv.Close()
	_, err := newTestClient(t, srv).ClipSchedules(context.Background(), "P", "C")
	if !IsStatus(err, 400) {
		t.Fatalf("err = %v", err)
	}
}
