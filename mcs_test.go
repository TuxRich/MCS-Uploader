package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeMCS records the requests it gets and answers as the MCS API does.
type fakeMCS struct {
	t            *testing.T
	calls        []string
	keys         []string
	posted       map[string]any
	deleteStatus int
	postStatus   int
}

func (f *fakeMCS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.keys = append(f.keys, r.Header.Get("X-API-Key"))
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v1/my/uploads/"):
		w.WriteHeader(f.deleteStatus)
		if f.deleteStatus == 200 {
			io.WriteString(w, `{"deleted":7}`)
		} else {
			io.WriteString(w, `{"error":"upload belongs to another club"}`)
		}
	case r.Method == "POST" && r.URL.Path == "/api/v1/results":
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			f.t.Errorf("Content-Type %q", ct)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &f.posted); err != nil {
			f.t.Errorf("bad JSON posted: %v", err)
		}
		w.WriteHeader(f.postStatus)
		if f.postStatus == 201 {
			io.WriteString(w, `{"upload_id":41,"imported":2,"track_id":4,"club_id":2}`)
		} else {
			io.WriteString(w, `{"error":"unknown track_id"}`)
		}
	case r.URL.Path == "/api/v1/tracks/4/leaderboard":
		io.WriteString(w, `{"track_id":4,"track":"Cream First","leaderboard":[
			{"rank":1,"pilot_id":3,"pilot":"Alice","club_id":2,"club":"Mine","best_seconds":48.48,"best_time":"48.48","attempts":3,"laps":{"lap1":16,"best_lap":15.9}},
			{"rank":2,"pilot_id":5,"pilot":"Zed","club_id":9,"club":"Other","best_seconds":50,"best_time":"50.00","attempts":1}]}`)
	case r.URL.Path == "/api/v1/tracks":
		io.WriteString(w, `{"tracks":[{"id":4,"name":"Cream First","status":"active","tags":[]}]}`)
	case r.URL.Path == "/api/v1/me":
		io.WriteString(w, `{"user_id":1,"email":"x","role":"director","club_id":2,"club":"Mine"}`)
	default:
		w.WriteHeader(404)
		io.WriteString(w, `{"error":"not found"}`)
	}
}

func newFake(t *testing.T) (*fakeMCS, mcsAPI) {
	f := &fakeMCS{t: t, deleteStatus: 200, postStatus: 201}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, settings{BaseURL: srv.URL + "/", APIKey: "mcs_test"}.api()
}

var testRows = []mcsRow{
	{Pilot: "Alice", Time: 61.5, Lap1: 20, Lap2: 20.5, Lap3: 21, BestLap: 20, Lap1Sectors: []float64{8, 12}},
	{Pilot: "Bob", Time: 70},
}

func TestUploadReplacesPrevious(t *testing.T) {
	f, api := newFake(t)
	id, imported, err := api.upload(4, 0, testRows, 7)
	if err != nil || id != 41 || imported != 2 {
		t.Fatalf("upload: id=%d imported=%d err=%v", id, imported, err)
	}
	if got := strings.Join(f.calls, ", "); got != "DELETE /api/v1/my/uploads/7, POST /api/v1/results" {
		t.Errorf("calls: %s", got)
	}
	for _, k := range f.keys {
		if k != "mcs_test" {
			t.Errorf("API key sent as %q", k)
		}
	}
	if f.posted["track_id"] != 4.0 {
		t.Errorf("track_id: %v", f.posted["track_id"])
	}
	if _, has := f.posted["club_id"]; has {
		t.Error("club_id sent though it is 0 (the key's own club)")
	}
	rows := f.posted["rows"].([]any)
	alice, bob := rows[0].(map[string]any), rows[1].(map[string]any)
	if alice["pilot"] != "Alice" || alice["time"] != 61.5 || alice["lap3"] != 21.0 || alice["best_lap"] != 20.0 || len(alice["lap1_sectors"].([]any)) != 2 {
		t.Errorf("Alice's row: %v", alice)
	}
	if len(bob) != 2 {
		t.Errorf("Bob's row should be just pilot and time: %v", bob)
	}
}

func TestUploadFirstTimeAndAdminClub(t *testing.T) {
	f, api := newFake(t)
	if _, _, err := api.upload(4, 2, testRows, 0); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0] != "POST /api/v1/results" {
		t.Errorf("calls: %v", f.calls)
	}
	if f.posted["club_id"] != 2.0 {
		t.Errorf("club_id: %v", f.posted["club_id"])
	}
}

func TestUploadPreviousAlreadyGone(t *testing.T) {
	f, api := newFake(t)
	f.deleteStatus = 404
	if id, _, err := api.upload(4, 0, testRows, 7); err != nil || id != 41 {
		t.Errorf("a previous upload that is already gone shouldn't stop the upload: id=%d err=%v", id, err)
	}
}

func TestUploadStopsIfPreviousCantBeRemoved(t *testing.T) {
	f, api := newFake(t)
	f.deleteStatus = 403
	_, _, err := api.upload(4, 0, testRows, 7)
	if err == nil || !strings.Contains(err.Error(), "belongs to another club") {
		t.Errorf("err: %v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("posted anyway: %v", f.calls)
	}
}

func TestUploadError(t *testing.T) {
	f, api := newFake(t)
	f.postStatus = 400
	_, _, err := api.upload(4, 0, testRows, 0)
	if err == nil || err.Error() != "unknown track_id (400)" {
		t.Errorf("err: %v", err)
	}
}

func TestReads(t *testing.T) {
	f, api := newFake(t)
	board, err := api.leaderboard(4)
	if err != nil || len(board) != 2 || board[0].Pilot != "Alice" || board[0].ClubID != 2 || board[0].BestSeconds != 48.48 ||
		board[0].Laps == nil || board[0].Laps.BestLap != 15.9 || board[1].Laps != nil {
		t.Errorf("leaderboard: %+v, %v", board, err)
	}
	tracks, err := api.tracks()
	if err != nil || len(tracks) != 1 || tracks[0].ID != 4 || tracks[0].Name != "Cream First" {
		t.Errorf("tracks: %+v, %v", tracks, err)
	}
	for _, k := range f.keys {
		if k != "" {
			t.Error("the API key was sent with a public request")
		}
	}
	me, err := api.me()
	if err != nil || me.ClubID != 2 || me.Club != "Mine" || f.keys[len(f.keys)-1] != "mcs_test" {
		t.Errorf("me: %+v, %v", me, err)
	}
	if _, err := api.leaderboard(99); err == nil || err.Error() != "not found (404)" {
		t.Errorf("missing track: %v", err)
	}
}

func TestTrackChoices(t *testing.T) {
	listed := []mcsTrack{{4, "Cream First"}, {7, "Autumn"}}
	if c, i := trackChoices(listed, 7, "Autumn"); len(c) != 2 || i != 1 {
		t.Errorf("listed track: %v, %d", c, i)
	}
	if c, i := trackChoices(listed, 0, ""); len(c) != 2 || i != -1 {
		t.Errorf("no track chosen: %v, %d", c, i)
	}
	// A staging track isn't listed, and the list may not have downloaded.
	if c, i := trackChoices(listed, 9, "Staging"); len(c) != 3 || i != 2 || c[2].Name != "Staging" || len(listed) != 2 {
		t.Errorf("unlisted track: %v, %d", c, i)
	}
	if c, i := trackChoices(nil, 9, "Staging"); len(c) != 1 || i != 0 {
		t.Errorf("no list: %v, %d", c, i)
	}
	if got := (settings{}).api().apiKeyPage(); got != "https://mcs.fpv-events.com/account/password" {
		t.Errorf("API key page: %s", got)
	}
}

func TestUploadTicks(t *testing.T) {
	rows := []mcsRow{{Pilot: "Alice", Time: 60}, {Pilot: "Bob", Time: 61}, {Pilot: "Cara", Time: 62}}
	// Bob was left out last time; Dave, also left out, isn't in the event now.
	ticks := uploadTicks(rows, []string{" bob", "Dave"})
	if len(ticks) != 3 || !ticks[0] || ticks[1] || !ticks[2] {
		t.Fatalf("ticks: %v", ticks)
	}
	ticks[2] = false // Cara unticked too
	upload, skipped := tickedRows(rows, ticks)
	if len(upload) != 1 || upload[0].Pilot != "Alice" || len(skipped) != 2 || skipped[0] != "Bob" || skipped[1] != "Cara" {
		t.Errorf("upload %v, skipped %v", upload, skipped)
	}
	if all := uploadTicks(rows, nil); !all[0] || !all[1] || !all[2] {
		t.Errorf("nothing left out before, so all ticked: %v", all)
	}
	if got := (settings{}).api().dvrPage(); got != "https://mcs.fpv-events.com/director/dvr" {
		t.Errorf("DVR page: %s", got)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "mcs.json")
	s := loadSettings(file) // no file yet
	if s.Uploads == nil || s.api().Base != mcsDefaultBaseURL || s.FlagLapUnder != 9 || !s.VideoTimer || !s.VideoLogo || s.MinHeight != 720 {
		t.Fatalf("defaults: %+v", s)
	}
	// A settings file from before the lap limit existed gets the default; one
	// that turns it off keeps it off.
	old := filepath.Join(t.TempDir(), "old.json")
	os.WriteFile(old, []byte(`{"api_key":"k","track_id":4}`), 0600)
	if got := loadSettings(old); got.FlagLapUnder != 9 || got.APIKey != "k" {
		t.Errorf("older file: %+v", got)
	}
	os.WriteFile(old, []byte(`{"api_key":"k","flag_lap_under":0}`), 0600)
	if got := loadSettings(old); got.FlagLapUnder != 0 {
		t.Errorf("limit turned off: %+v", got)
	}
	s.APIKey, s.TrackID, s.ShowBoard = "k", 4, true
	s.Uploads["ev"] = 41
	s.Skipped["ev"] = []string{"Bob"}
	if err := s.save(file); err != nil {
		t.Fatal(err)
	}
	got := loadSettings(file)
	if got.APIKey != "k" || got.TrackID != 4 || !got.ShowBoard || got.Uploads["ev"] != 41 || len(got.Skipped["ev"]) != 1 {
		t.Errorf("reloaded: %+v", got)
	}
}
