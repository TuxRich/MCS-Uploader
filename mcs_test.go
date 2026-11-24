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
		// As MCS answers: by whose the key is.
		switch r.Header.Get("X-API-Key") {
		case "mcs_test":
			io.WriteString(w, `{"user_id":1,"email":"x","role":"director","club_id":2,"club":"Mine"}`)
		case "mcs_admin":
			io.WriteString(w, `{"user_id":2,"email":"y","role":"admin","club_id":0,"club":""}`)
		case "mcs_designer":
			w.WriteHeader(403)
			io.WriteString(w, `{"error":"this account type cannot use the API"}`)
		default:
			w.WriteHeader(401)
			io.WriteString(w, `{"error":"invalid API key"}`)
		}
	case r.URL.Path == "/api/v1/clubs":
		io.WriteString(w, `{"clubs":[{"id":2,"name":"Mine","country":"GB"},{"id":9,"name":"Other","country":"GB"}]}`)
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
	if s.Uploads == nil || s.api().Base != mcsDefaultBaseURL || s.FlagLapUnder != 9 || !s.VideoTimer || !s.VideoLogo || !s.VideoUpload || s.MinHeight != 720 {
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
	// Putting videos on YouTube is ticked to begin with, also for a file from
	// before it was; once unticked it stays unticked.
	if got := loadSettings(old); !got.VideoUpload {
		t.Errorf("older file: videos not uploaded by default: %+v", got)
	}
	s.VideoUpload = false
	if err := s.save(file); err != nil {
		t.Fatal(err)
	}
	if got := loadSettings(file); got.VideoUpload {
		t.Errorf("unticking the YouTube upload wasn't kept: %+v", got)
	}
}

func TestKeyShaped(t *testing.T) {
	whole := "mcs_" + strings.Repeat("a", 32)
	for text, want := range map[string]bool{
		whole:                                  true,
		"mcs_AbC-_0123456789abcdefghijklmnopq": true,
		"":                                     false,
		whole[:35]:                             false, // one more character to type
		whole + "a":                            false,
		"abc_" + strings.Repeat("a", 32):       false,
	} {
		if got := keyShaped(text); got != want {
			t.Errorf("keyShaped(%q) = %v, want %v", text, got, want)
		}
	}
}

// Whose a key is, as the settings and the upload window ask MCS, and what
// they say about the answer.
func TestWhoseKey(t *testing.T) {
	_, api := newFake(t)
	for key, want := range map[string]struct {
		admin  bool
		status string
	}{
		"mcs_test":     {false, "A race director's key, for Mine."},
		"mcs_admin":    {true, "An admin's key: it can upload for any club."},
		"mcs_designer": {false, "This key's account can't upload times: only race directors and admins can."},
		"mcs_wrong":    {false, "MCS doesn't know this key."},
	} {
		who, err := mcsAPI{Base: api.Base, Key: key}.me()
		if got := err == nil && who.admin(); got != want.admin {
			t.Errorf("%s: admin %v, want %v (%+v, %v)", key, got, want.admin, who, err)
		}
		if got := keyStatus(who, err); got != want.status {
			t.Errorf("%s: %q, want %q", key, got, want.status)
		}
	}
	// A race director with no club yet, and MCS not answering at all.
	if got := keyStatus(mcsMe{Role: "director"}, nil); got != "A race director's key." {
		t.Errorf("no club: %q", got)
	}
	_, err := mcsAPI{Base: "http://127.0.0.1:1", Key: "mcs_test"}.me()
	if got := keyStatus(mcsMe{}, err); err == nil || !strings.HasPrefix(got, "The key couldn't be checked with MCS: ") {
		t.Errorf("no answer: %q, %v", got, err)
	}

	clubs, err := api.clubs()
	if err != nil || len(clubs) != 2 || clubs[1] != (mcsClub{9, "Other"}) {
		t.Errorf("clubs: %+v, %v", clubs, err)
	}
}

func TestClubChoices(t *testing.T) {
	listed := []mcsClub{{2, "Mine"}, {9, "Other"}}
	if c, i := clubChoices(listed, mcsClub{9, "Other"}); len(c) != 2 || i != 1 {
		t.Errorf("listed club: %v, %d", c, i)
	}
	if c, i := clubChoices(listed, mcsClub{}); len(c) != 2 || i != -1 {
		t.Errorf("no club chosen: %v, %d", c, i)
	}
	// The list may not have downloaded, or the club has gone from it.
	if c, i := clubChoices(nil, mcsClub{9, "Other"}); len(c) != 1 || i != 0 || c[0].Name != "Other" {
		t.Errorf("no list: %v, %d", c, i)
	}
	if c, i := clubChoices(listed, mcsClub{5, "Gone"}); len(c) != 3 || i != 2 || len(listed) != 2 {
		t.Errorf("unlisted club: %v, %d", c, i)
	}
	if c, i := clubChoices(nil, mcsClub{}); len(c) != 0 || i != -1 {
		t.Errorf("nothing at all: %v, %d", c, i)
	}
}

func TestClubFor(t *testing.T) {
	director := mcsMe{Role: "director", ClubID: 2, Club: "Mine"}
	admin := mcsMe{Role: "admin"}
	s := settings{ClubID: 9, ClubName: "Other", UploadClubs: map[string]mcsClub{"ev": {5, "Third"}}}

	// A race director's key has its own club, whatever the settings hold.
	if got := s.clubFor("ev", director, true); got != (mcsClub{2, "Mine"}) {
		t.Errorf("director: %+v", got)
	}
	// An admin's: the club the event was last uploaded for, else the usual one.
	if got := s.clubFor("ev", admin, true); got != (mcsClub{5, "Third"}) {
		t.Errorf("admin, event uploaded before: %+v", got)
	}
	if got := s.clubFor("another", admin, true); got != (mcsClub{9, "Other"}) {
		t.Errorf("admin, event not uploaded: %+v", got)
	}
	// No usual club chosen: none, unless the admin's account has one.
	if got := (settings{}).clubFor("ev", admin, true); got.ID != 0 {
		t.Errorf("admin, nothing chosen: %+v", got)
	}
	if got := (settings{}).clubFor("ev", mcsMe{Role: "admin", ClubID: 2, Club: "Mine"}, true); got != (mcsClub{2, "Mine"}) {
		t.Errorf("admin with a club of their own: %+v", got)
	}
	// Before MCS has said whose the key is, what the settings hold.
	if got := s.clubFor("another", mcsMe{}, false); got != (mcsClub{9, "Other"}) {
		t.Errorf("key not checked yet: %+v", got)
	}
	if got := (settings{}).clubFor("ev", mcsMe{}, false); got.ID != 0 {
		t.Errorf("nothing known: %+v", got)
	}
}

func TestReplaceNote(t *testing.T) {
	mine, other := mcsClub{2, "Mine"}, mcsClub{9, "Other"}
	const plain = "This replaces the event's previous upload, so a pilot unticked now is removed from it."
	for _, c := range []struct{ was, now mcsClub }{{mine, mine}, {mcsClub{}, mine}, {mine, mcsClub{}}, {mcsClub{}, mcsClub{}}} {
		if got := replaceNote(c.was, c.now); got != plain {
			t.Errorf("%+v: %q", c, got)
		}
	}
	// Uploading an event again for another club moves its times.
	got := replaceNote(mine, other)
	if !strings.Contains(got, "which was for Mine") || !strings.Contains(got, "go on for Other") {
		t.Errorf("another club: %q", got)
	}
}

func TestUploadClubsKept(t *testing.T) {
	file := filepath.Join(t.TempDir(), "mcs.json")
	s := loadSettings(file)
	if s.UploadClubs == nil {
		t.Fatal("no map of the clubs events were uploaded for")
	}
	s.UploadClubs["ev"] = mcsClub{9, "Other"}
	if err := s.save(file); err != nil {
		t.Fatal(err)
	}
	if got := loadSettings(file).UploadClubs["ev"]; got != (mcsClub{9, "Other"}) {
		t.Errorf("reloaded: %+v", got)
	}
	// A race director's settings, with none, don't get an empty entry.
	data, _ := os.ReadFile(file)
	if delete(s.UploadClubs, "ev"); s.save(file) != nil {
		t.Fatal("save")
	}
	if after, _ := os.ReadFile(file); strings.Contains(string(after), "upload_clubs") || !strings.Contains(string(data), "upload_clubs") {
		t.Errorf("upload_clubs written when empty, or not when set:\n%s\n%s", data, after)
	}
}
