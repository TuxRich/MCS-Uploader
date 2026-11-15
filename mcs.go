package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// --- Settings ---

const mcsDefaultBaseURL = "https://mcs.fpv-events.com"

// settings is kept in mcs.json beside the program, so the API key, chosen
// track and each event's upload id survive restarts.
type settings struct {
	BaseURL      string              `json:"base_url,omitempty"` // override for testing; empty = live site
	APIKey       string              `json:"api_key"`
	TrackID      int64               `json:"track_id"`
	TrackName    string              `json:"track_name"`
	ClubID       int64               `json:"club_id,omitempty"` // only needed for admin keys; 0 = key's own club
	ClubName     string              `json:"club_name,omitempty"`
	Uploads      map[string]int64    `json:"uploads"`                 // event GUID -> MCS upload_id
	Skipped      map[string][]string `json:"skipped,omitempty"`       // event GUID -> pilots left out of its upload
	EventsFolder string              `json:"events_folder,omitempty"` // set only when FPVTrackside's own can't be found
	FlagLapUnder float64             `json:"flag_lap_under"`          // flag laps quicker than this many seconds; 0 = don't
	ShowBoard    bool                `json:"show_leaderboard"`
	ClubOnly     bool                `json:"club_only"`
	VideoFolder  string              `json:"video_folder,omitempty"`      // where videos were last exported to
	WholeFrame   bool                `json:"video_whole_frame,omitempty"` // export the whole recorded picture, not just the pilot's
	FFmpegPath   string              `json:"ffmpeg_path,omitempty"`       // ffmpeg.exe, if it isn't on the PATH
	VideoTimer   bool                `json:"video_timer"`                 // draw a timer on exported videos
}

func loadSettings(file string) settings {
	s := settings{FlagLapUnder: defaultLapLimit, VideoTimer: true} // unless the file says otherwise
	if data, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	if s.Uploads == nil {
		s.Uploads = map[string]int64{}
	}
	if s.Skipped == nil {
		s.Skipped = map[string][]string{}
	}
	return s
}

func (s settings) save(file string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0600)
}

func (s settings) api() mcsAPI {
	base := mcsDefaultBaseURL
	if s.BaseURL != "" {
		base = strings.TrimRight(s.BaseURL, "/")
	}
	return mcsAPI{Base: base, Key: s.APIKey}
}

// --- The MCS API ---

var mcsClient = &http.Client{Timeout: 15 * time.Second}

type mcsAPI struct {
	Base string
	Key  string
}

// apiError is a reply from MCS that wasn't a success.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string { return fmt.Sprintf("%s (%d)", e.Message, e.Status) }

// do sends a request to the MCS API and decodes a successful reply into out.
// The API key goes only with requests that need it.
func (a mcsAPI) do(method, path string, auth bool, body, out any) error {
	var rdr io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, a.Base+"/api/v1"+path, rdr)
	if err != nil {
		return err
	}
	if auth {
		req.Header.Set("X-API-Key", a.Key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := mcsClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) != nil || e.Error == "" {
			e.Error = strings.TrimSpace(string(data))
			if len(e.Error) > 200 {
				e.Error = e.Error[:200]
			}
			if e.Error == "" {
				e.Error = resp.Status
			}
		}
		return &apiError{resp.StatusCode, e.Error}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

type mcsTrack struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func (a mcsAPI) tracks() ([]mcsTrack, error) {
	var body struct {
		Tracks []mcsTrack `json:"tracks"`
	}
	err := a.do("GET", "/tracks", false, nil, &body)
	return body.Tracks, err
}

// trackChoices is what a track list offers: the tracks MCS lists, plus the
// chosen one if it isn't among them, as a track still in staging isn't listed
// and the list may not have been downloaded. It returns the choices and which
// is the chosen one, -1 if none is.
func trackChoices(listed []mcsTrack, chosenID int64, chosenName string) ([]mcsTrack, int) {
	choices := append([]mcsTrack(nil), listed...)
	for i, t := range choices {
		if t.ID == chosenID {
			return choices, i
		}
	}
	if chosenID == 0 {
		return choices, -1
	}
	return append(choices, mcsTrack{chosenID, chosenName}), len(choices)
}

// apiKeyPage is where a signed-in user gets their API key.
func (a mcsAPI) apiKeyPage() string { return a.Base + "/account/password" }

// dvrPage is where a race director adds DVR links to uploaded times.
func (a mcsAPI) dvrPage() string { return a.Base + "/director/dvr" }

// uploadTicks says which rows start ticked when an upload is confirmed: all
// but those of the pilots left out of the event's upload last time.
func uploadTicks(rows []mcsRow, skipped []string) []bool {
	out := map[string]bool{}
	for _, name := range skipped {
		out[nameKey(name)] = true
	}
	ticks := make([]bool, len(rows))
	for i, r := range rows {
		ticks[i] = !out[nameKey(r.Pilot)]
	}
	return ticks
}

// tickedRows splits rows into those to upload and the names of the pilots
// left out.
func tickedRows(rows []mcsRow, ticks []bool) (upload []mcsRow, skipped []string) {
	for i, r := range rows {
		if ticks[i] {
			upload = append(upload, r)
		} else {
			skipped = append(skipped, r.Pilot)
		}
	}
	return upload, skipped
}

type mcsClub struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func (a mcsAPI) clubs() ([]mcsClub, error) {
	var body struct {
		Clubs []mcsClub `json:"clubs"`
	}
	err := a.do("GET", "/clubs", false, nil, &body)
	return body.Clubs, err
}

// mcsMe is who the API key belongs to.
type mcsMe struct {
	Role   string `json:"role"`
	ClubID int64  `json:"club_id"`
	Club   string `json:"club"`
}

func (a mcsAPI) me() (mcsMe, error) {
	var me mcsMe
	err := a.do("GET", "/me", true, nil, &me)
	return me, err
}

// mcsEntry is one pilot's place on a track's MCS leaderboard.
type mcsEntry struct {
	Rank        int     `json:"rank"`
	PilotID     int64   `json:"pilot_id"`
	Pilot       string  `json:"pilot"`
	ClubID      int64   `json:"club_id"`
	Club        string  `json:"club"`
	BestSeconds float64 `json:"best_seconds"`
	Laps        *struct {
		BestLap float64 `json:"best_lap"`
	} `json:"laps"`
}

// leaderboard downloads a track's leaderboard, all clubs.
func (a mcsAPI) leaderboard(trackID int64) ([]mcsEntry, error) {
	var body struct {
		Leaderboard []mcsEntry `json:"leaderboard"`
	}
	err := a.do("GET", fmt.Sprintf("/tracks/%d/leaderboard", trackID), false, nil, &body)
	return body.Leaderboard, err
}

// upload sends an event's results to a track. MCS keeps every upload it is
// sent, so an event's previous upload (previous, 0 if none) is deleted first:
// uploading again then updates the event's results rather than stacking a
// second set. It returns the new upload's id and how many rows MCS took.
func (a mcsAPI) upload(trackID, clubID int64, rows []mcsRow, previous int64) (uploadID int64, imported int, err error) {
	if previous != 0 {
		err := a.do("DELETE", fmt.Sprintf("/my/uploads/%d", previous), true, nil, nil)
		var ae *apiError
		// Already gone (removed on the site, say) is as good as deleted.
		if err != nil && !(errors.As(err, &ae) && ae.Status == http.StatusNotFound) {
			return 0, 0, fmt.Errorf("could not remove previous upload %d: %w", previous, err)
		}
	}
	payload := struct {
		TrackID int64    `json:"track_id"`
		ClubID  int64    `json:"club_id,omitempty"` // admin keys only
		Rows    []mcsRow `json:"rows"`
	}{trackID, clubID, rows}
	var resp struct {
		UploadID int64 `json:"upload_id"`
		Imported int   `json:"imported"`
	}
	if err := a.do("POST", "/results", true, payload, &resp); err != nil {
		return 0, 0, err
	}
	if resp.UploadID == 0 {
		return 0, 0, errors.New("MCS didn't return an upload id")
	}
	return resp.UploadID, resp.Imported, nil
}
