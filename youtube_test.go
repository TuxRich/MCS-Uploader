package main

import (
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
)

// fakeVideoSite stands in for the MCS site's video upload calls, and for
// YouTube's address the video is sent to.
type fakeVideoSite struct {
	t   *testing.T
	srv *httptest.Server

	mu            sync.Mutex
	calls         []string
	videoUpload   bool // what /me says
	youtubeStatus int  // what "YouTube" answers a video sent straight to it
	startBody     map[string]any
	direct        string // the video YouTube was sent directly
	directKey     string // the API key that came with it, which should be none
	throughSite   string // the video sent through the site
	throughKey    string
	finished      bool
	results       string // the JSON for the event's upload
}

func newFakeVideoSite(t *testing.T) (*fakeVideoSite, mcsAPI) {
	f := &fakeVideoSite{t: t, videoUpload: true, youtubeStatus: http.StatusCreated,
		results: `{"upload":{"id":9},"results":[{"id":36,"pilot":"Alice","seconds":40.58},{"id":37,"pilot":"Bob","seconds":41.282,"video_url":"https://youtu.be/old"}]}`}
	mux := http.NewServeMux()
	note := func(r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
	}
	mux.HandleFunc("GET /api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		note(r)
		fmt.Fprintf(w, `{"role":"director","club_id":2,"club":"Mine","video_upload":%v}`, f.videoUpload)
	})
	mux.HandleFunc("GET /api/v1/my/uploads/9", func(w http.ResponseWriter, r *http.Request) {
		note(r)
		io.WriteString(w, f.results)
	})
	mux.HandleFunc("GET /api/v1/my/uploads/404", func(w http.ResponseWriter, r *http.Request) {
		note(r)
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"upload not found"}`)
	})
	mux.HandleFunc("POST /api/v1/my/results/36/video", func(w http.ResponseWriter, r *http.Request) {
		note(r)
		if r.Header.Get("X-API-Key") != "mcs_test" {
			t.Errorf("the upload was started without the API key")
		}
		json.NewDecoder(r.Body).Decode(&f.startBody)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"upload_id":"up1","upload_url":"%s/youtube/session","content_type":"video/mp4","title":"MCS - Track - Alice - 40.58"}`, f.srv.URL)
	})
	mux.HandleFunc("POST /api/v1/my/results/37/video", func(w http.ResponseWriter, r *http.Request) {
		note(r)
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":"this club has started 40 video uploads today, which is the limit; try again tomorrow"}`)
	})
	mux.HandleFunc("PUT /youtube/session", func(w http.ResponseWriter, r *http.Request) {
		note(r)
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.directKey = r.Header.Get("X-API-Key") + r.Header.Get("Authorization")
		status := f.youtubeStatus
		if status == http.StatusCreated {
			f.direct = string(body)
		}
		f.mu.Unlock()
		if r.ContentLength != int64(len(body)) || r.Header.Get("Content-Type") != "video/mp4" {
			t.Errorf("YouTube was sent length %d of %d, type %q", r.ContentLength, len(body), r.Header.Get("Content-Type"))
		}
		w.WriteHeader(status)
		if status == http.StatusCreated {
			io.WriteString(w, `{"id":"vid1"}`)
		} else {
			io.WriteString(w, `{"error":{"code":401,"message":"Login Required."}}`)
		}
	})
	mux.HandleFunc("POST /api/v1/my/video-uploads/up1/finish", func(w http.ResponseWriter, r *http.Request) {
		note(r)
		f.mu.Lock()
		f.finished = true
		f.mu.Unlock()
		io.WriteString(w, `{"id":36,"pilot":"Alice","video_id":"vid1","video_url":"https://youtu.be/vid1"}`)
	})
	mux.HandleFunc("PUT /api/v1/my/video-uploads/up1", func(w http.ResponseWriter, r *http.Request) {
		note(r)
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.throughSite, f.throughKey = string(body), r.Header.Get("X-API-Key")
		f.mu.Unlock()
		io.WriteString(w, `{"id":36,"pilot":"Alice","video_id":"vid2","video_url":"https://youtu.be/vid2"}`)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f, settings{BaseURL: f.srv.URL, APIKey: "mcs_test"}.api()
}

func videoFile(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(file, []byte("the video"), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

// The usual way: the site starts the upload, the video goes straight to
// YouTube with no key of ours, and the site is told to link it.
func TestUploadVideoDirect(t *testing.T) {
	f, api := newFakeVideoSite(t)
	link, err := api.uploadVideo(context.Background(), 36, videoFile(t))
	if err != nil || link != "https://youtu.be/vid1" {
		t.Fatalf("uploadVideo = %q, %v", link, err)
	}
	if got := strings.Join(f.calls, ", "); got != "POST /api/v1/my/results/36/video, PUT /youtube/session, POST /api/v1/my/video-uploads/up1/finish" {
		t.Errorf("calls: %s", got)
	}
	if f.startBody["size"] != 9.0 || f.startBody["content_type"] != "video/mp4" {
		t.Errorf("the upload was started with %v", f.startBody)
	}
	if f.direct != "the video" {
		t.Errorf("YouTube received %q", f.direct)
	}
	if f.directKey != "" {
		t.Errorf("the MCS API key was sent to YouTube: %q", f.directKey)
	}
	if f.throughSite != "" {
		t.Error("the video was also sent through the site")
	}
}

// If YouTube won't take the video from here, it goes through the site, with
// the key, and the site's answer carries the link.
func TestUploadVideoThroughTheSite(t *testing.T) {
	f, api := newFakeVideoSite(t)
	f.youtubeStatus = http.StatusUnauthorized
	link, err := api.uploadVideo(context.Background(), 36, videoFile(t))
	if err != nil || link != "https://youtu.be/vid2" {
		t.Fatalf("uploadVideo = %q, %v", link, err)
	}
	if got := strings.Join(f.calls, ", "); got != "POST /api/v1/my/results/36/video, PUT /youtube/session, PUT /api/v1/my/video-uploads/up1" {
		t.Errorf("calls: %s", got)
	}
	if f.throughSite != "the video" || f.throughKey != "mcs_test" {
		t.Errorf("the site received %q with key %q", f.throughSite, f.throughKey)
	}
	if f.finished {
		t.Error("finish was called as well, though the site links a video it passes on")
	}
}

// What stops an upload is reported in the site's or YouTube's own words.
func TestUploadVideoRefused(t *testing.T) {
	f, api := newFakeVideoSite(t)
	// The site refuses to start one: nothing is sent anywhere.
	_, err := api.uploadVideo(context.Background(), 37, videoFile(t))
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("a refused start gave %v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("after a refused start: %v", f.calls)
	}
	// YouTube has lost the upload: that isn't cured by going through the site.
	f.youtubeStatus = http.StatusNotFound
	_, err = api.uploadVideo(context.Background(), 36, videoFile(t))
	if err == nil || !strings.Contains(err.Error(), "YouTube didn't take the video") {
		t.Errorf("an upload YouTube has lost gave %v", err)
	}
	if f.throughSite != "" {
		t.Error("the video was sent through the site for an upload YouTube no longer has")
	}
	// A file that isn't there.
	if _, err := api.uploadVideo(context.Background(), 36, filepath.Join(t.TempDir(), "none.mp4")); err == nil {
		t.Error("a missing file was uploaded")
	}
}

// Which videos can go against which times: only where MCS has exactly that
// time for that pilot and no video yet.
func TestMatchVideos(t *testing.T) {
	results := []mcsResult{
		{ID: 36, Pilot: "Alice", Seconds: 40.58},
		{ID: 37, Pilot: "Bob", Seconds: 41.282, VideoURL: "https://youtu.be/old"},
		{ID: 38, Pilot: "Cara", Seconds: 50.481},
	}
	runs := []lapRun{
		{Name: " alice", Total: 40.58}, // the same pilot, the same time
		{Name: "Bob", Total: 41.282},   // has a video already
		{Name: "Cara", Total: 49.9},    // has gone quicker since the times were uploaded
		{Name: "Dave", Total: 60},      // wasn't in the upload
	}
	got := matchVideos(runs, results)
	if got[0] != (videoMatch{ResultID: 36}) {
		t.Errorf("Alice: %+v", got[0])
	}
	if got[1].ResultID != 37 || got[1].Why != "Already has a video on MCS" {
		t.Errorf("Bob: %+v", got[1])
	}
	if got[2].ResultID != 38 || got[2].Why != "MCS has 50.481: upload the times again first" {
		t.Errorf("Cara: %+v", got[2])
	}
	if got[3].ResultID != 0 || got[3].Why != "No time on MCS: upload the times first" {
		t.Errorf("Dave: %+v", got[3])
	}
}

// Whether uploading is on offer at all, and the reason given when it isn't.
func TestCheckVideoUpload(t *testing.T) {
	f, api := newFakeVideoSite(t)
	runs := []lapRun{{Name: "Alice", Total: 40.58}, {Name: "Bob", Total: 41.282}}

	offer := checkVideoUpload(api, true, 9, runs)
	if !offer.Available || len(offer.Matches) != 2 || offer.Matches[0].ResultID != 36 || offer.Matches[1].Why == "" {
		t.Errorf("with everything in place: %+v", offer)
	}

	for why, check := range map[string]func() videoOffer{
		"API key":           func() videoOffer { return checkVideoUpload(api, false, 9, runs) },
		"Upload this event": func() videoOffer { return checkVideoUpload(api, true, 0, runs) },
		"no longer on MCS":  func() videoOffer { return checkVideoUpload(api, true, 404, runs) },
		"no YouTube channel": func() videoOffer {
			f.videoUpload = false
			defer func() { f.videoUpload = true }()
			return checkVideoUpload(api, true, 9, runs)
		},
		"couldn't be asked": func() videoOffer {
			return checkVideoUpload(settings{BaseURL: "http://127.0.0.1:1", APIKey: "k"}.api(), true, 9, runs)
		},
	} {
		if offer := check(); offer.Available || !strings.Contains(offer.Why, why) {
			t.Errorf("expected to be told about %q, got %+v", why, offer)
		}
	}
}
