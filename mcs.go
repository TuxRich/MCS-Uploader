package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// --- Settings ---

const mcsDefaultBaseURL = "https://mcs.fpv-events.com"

const (
	settingsName   = "mcs.json"
	settingsFolder = "MCS Uploader" // in the user's own settings folder (%APPDATA%)
)

// settingsPath is where the settings are kept. A mcs.json beside the program
// is used if there is one, which is where it always used to be. Otherwise
// they go in the user's own settings folder: an installed program's folder
// isn't always one it may write to, and settings kept there outlive an
// update or a reinstall.
func settingsPath(exeDir, userDir string) string {
	beside := filepath.Join(exeDir, settingsName)
	if st, err := os.Stat(beside); (err == nil && !st.IsDir()) || userDir == "" {
		return beside
	}
	return filepath.Join(userDir, settingsFolder, settingsName)
}

// settings is kept in mcs.json (see settingsPath), so the API key, chosen
// track and each event's upload id survive restarts.
type settings struct {
	BaseURL      string              `json:"base_url,omitempty"` // override for testing; empty = live site
	APIKey       string              `json:"api_key"`
	TrackID      int64               `json:"track_id"`
	TrackName    string              `json:"track_name"`
	ClubID       int64               `json:"club_id,omitempty"` // an admin key's usual club to upload for; a race director's key has its own
	ClubName     string              `json:"club_name,omitempty"`
	Uploads      map[string]int64    `json:"uploads"`                 // event GUID -> MCS upload_id
	MoreUploads  map[string][]int64  `json:"more_uploads,omitempty"`  // event GUID -> its later uploads: see uploadIDs
	UploadClubs  map[string]mcsClub  `json:"upload_clubs,omitempty"`  // event GUID -> the club an admin key uploaded it for
	Skipped      map[string][]string `json:"skipped,omitempty"`       // event GUID -> pilots held back from its upload
	EventsFolder string              `json:"events_folder,omitempty"` // set only when FPVTrackside's own can't be found
	FlagLapUnder float64             `json:"flag_lap_under"`          // flag laps quicker than this many seconds; 0 = don't
	ShowBoard    bool                `json:"show_leaderboard"`
	ClubOnly     bool                `json:"club_only"`
	VideoFolder  string              `json:"video_folder,omitempty"`      // where videos were last exported to
	WholeFrame   bool                `json:"video_whole_frame,omitempty"` // export the whole recorded picture, not just the pilot's
	FFmpegPath   string              `json:"ffmpeg_path,omitempty"`       // ffmpeg.exe, if it isn't on the PATH
	VideoTimer   bool                `json:"video_timer"`                 // draw a timer and lap times on exported videos
	VideoLogo    bool                `json:"video_logo"`                  // put the MCS logo in the corner of exported videos
	VideoUpload  bool                `json:"video_upload"`                // put exported videos on the league's YouTube channel through MCS
	MinHeight    int                 `json:"video_min_height"`            // enlarge exported videos less than this high; 0 = don't
}

func loadSettings(file string) settings {
	// Unless the file says otherwise:
	s := settings{FlagLapUnder: defaultLapLimit, VideoTimer: true, VideoLogo: true, VideoUpload: true, MinHeight: defaultMinHeight}
	if data, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	if s.Uploads == nil {
		s.Uploads = map[string]int64{}
	}
	if s.MoreUploads == nil {
		s.MoreUploads = map[string][]int64{}
	}
	if s.UploadClubs == nil {
		s.UploadClubs = map[string]mcsClub{}
	}
	if s.Skipped == nil {
		s.Skipped = map[string][]string{}
	}
	return s
}

// clubFor is the club an event's pilots fly for on MCS, with ID 0 if that
// isn't known. who is whose the API key is, if known says MCS has been asked.
// A race director's key has one club, its own. An admin's key uploads for any
// club: the one the event was last uploaded for, or else the usual one, chosen
// in the settings.
func (s settings) clubFor(eventID string, who mcsMe, known bool) mcsClub {
	if known && !who.admin() {
		return mcsClub{who.ClubID, who.Club}
	}
	if c := s.UploadClubs[eventID]; c.ID != 0 {
		return c
	}
	if s.ClubID != 0 {
		return mcsClub{s.ClubID, s.ClubName}
	}
	return mcsClub{who.ClubID, who.Club}
}

// uploadIDs are an event's uploads on MCS: its first, and one more for each
// time pilots have been added to it since. Uploading an event again changes
// the times MCS has rather than sending them all afresh, but MCS can't add a
// time to an upload it already has, so the times of pilots new to the event go
// up as an upload of their own.
func (s settings) uploadIDs(eventID string) []int64 {
	var ids []int64
	if id := s.Uploads[eventID]; id != 0 {
		ids = append(ids, id)
	}
	return append(ids, s.MoreUploads[eventID]...)
}

// setUploadIDs records which uploads on MCS are an event's.
func (s settings) setUploadIDs(eventID string, ids []int64) {
	delete(s.Uploads, eventID)
	delete(s.MoreUploads, eventID)
	if len(ids) > 0 {
		s.Uploads[eventID] = ids[0]
	}
	if len(ids) > 1 {
		s.MoreUploads[eventID] = append([]int64(nil), ids[1:]...)
	}
}

func (s settings) save(file string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(file); dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
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
	req.Header.Set("User-Agent", userAgent())
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

// clubChoices is what a list of clubs offers: the clubs MCS lists, plus the
// chosen one if it isn't among them, as the list may not have been
// downloaded. It returns the choices and which is the chosen one, -1 if none
// is.
func clubChoices(listed []mcsClub, chosen mcsClub) ([]mcsClub, int) {
	choices := append([]mcsClub(nil), listed...)
	for i, c := range choices {
		if c.ID == chosen.ID {
			return choices, i
		}
	}
	if chosen.ID == 0 {
		return choices, -1
	}
	return append(choices, chosen), len(choices)
}

// mcsMe is who the API key belongs to.
type mcsMe struct {
	Role   string `json:"role"`
	ClubID int64  `json:"club_id"`
	Club   string `json:"club"`
	// VideoUpload is whether the site can put videos on the league's YouTube
	// channel: an admin has connected the channel.
	VideoUpload bool `json:"video_upload"`
}

// admin reports whether the key is an admin's. A race director's key uploads
// for its own club only; an admin's uploads for any club, and so has to say
// which each time.
func (m mcsMe) admin() bool { return m.Role == "admin" }

func (a mcsAPI) me() (mcsMe, error) {
	var me mcsMe
	err := a.do("GET", "/me", true, nil, &me)
	return me, err
}

// keyShaped reports whether text is as long as an API key from MCS and
// starts like one ("mcs_" and 32 more characters), so that a key can be
// checked with MCS as soon as it has been pasted or typed in full.
func keyShaped(text string) bool {
	return len(text) == 36 && strings.HasPrefix(text, "mcs_")
}

// keyStatus says in a line what MCS answered when asked whose a key is.
func keyStatus(who mcsMe, err error) string {
	var ae *apiError
	switch {
	case err == nil && who.admin():
		return "An admin's key: it can upload for any club."
	case err == nil && who.Club != "":
		return "A race director's key, for " + who.Club + "."
	case err == nil:
		return "A race director's key."
	case errors.As(err, &ae) && ae.Status == http.StatusUnauthorized:
		return "MCS doesn't know this key."
	case errors.As(err, &ae) && ae.Status == http.StatusForbidden:
		return "This key's account can't upload times: only race directors and admins can."
	}
	return fmt.Sprintf("The key couldn't be checked with MCS: %v.", err)
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
