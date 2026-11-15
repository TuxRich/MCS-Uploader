package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// --- FPVTrackside's files ---

type eventRecord struct {
	Name  string `json:"Name"`
	Start string `json:"Start"`
	ID    string `json:"ID"`
}

type pilotRecord struct {
	ID   string `json:"ID"`
	Name string `json:"Name"`
}

type raceLap struct {
	Detection     string  `json:"Detection"`
	LengthSeconds float64 `json:"LengthSeconds"`
	LapNumber     int     `json:"LapNumber"`
	StartTime     string  `json:"StartTime"`
	EndTime       string  `json:"EndTime"`
	ID            string  `json:"ID"`
}

type raceDetection struct {
	TimingSystemIndex int    `json:"TimingSystemIndex"`
	Time              string `json:"Time"`
	Pilot             string `json:"Pilot"`
	Valid             bool   `json:"Valid"`
	IsLapEnd          bool   `json:"IsLapEnd"`
	ID                string `json:"ID"`
}

type raceRecord struct {
	Laps       []raceLap       `json:"Laps"`
	Detections []raceDetection `json:"Detections"`
	Round      string          `json:"Round"`
	Valid      bool            `json:"Valid"`
	ID         string          `json:"ID"`
}

// readJSONFile reads one of Trackside's JSON files into v.
func readJSONFile(file string, v any) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return json.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), v)
}

// parseTracksideTime reads a time as Race.json writes it. The fraction of a
// second is optional.
func parseTracksideTime(s string) (time.Time, error) {
	return time.ParseInLocation("2006/01/02 15:04:05", s, time.UTC)
}

// --- Where the events are ---

// tracksideDefaultProfile is the profile FPVTrackside starts with.
const tracksideDefaultProfile = "Profile 1"

// readTracksideXML reads one of FPVTrackside's settings files into v.
func readTracksideXML(file string, v any) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return xml.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), v)
}

// eventsFolder works out where FPVTrackside keeps its events, as FPVTrackside
// does, from its folder (%LOCALAPPDATA%\FPVTrackside):
//
//   - data\GeneralSettings.xml names the profile it has loaded, "Profile 1"
//     unless it says otherwise;
//   - data\<profile>\ProfileSettings.xml has that profile's Event Storage
//     Location, "events" if it isn't set;
//   - a location that isn't a full path, such as "events/", is in
//     FPVTrackside's folder, and one starting ~ is in the user's home folder.
//
// It returns the folder and the profile.
func eventsFolder(tracksideDir string) (folder, profile string) {
	profile = tracksideDefaultProfile
	var general struct {
		Settings []struct {
			Profile string `xml:"Profile"`
		} `xml:"GeneralSettings"`
	}
	if err := readTracksideXML(filepath.Join(tracksideDir, "data", "GeneralSettings.xml"), &general); err != nil {
		log.Printf("Using FPVTrackside profile %q: %v", profile, err)
	} else if len(general.Settings) > 0 && strings.TrimSpace(general.Settings[0].Profile) != "" {
		profile = strings.TrimSpace(general.Settings[0].Profile)
	}

	location := "events"
	var settings struct {
		Settings []struct {
			EventStorageLocation string `xml:"EventStorageLocation"`
		} `xml:"ApplicationProfileSettings"`
	}
	if err := readTracksideXML(filepath.Join(tracksideDir, "data", profile, "ProfileSettings.xml"), &settings); err != nil {
		log.Printf("Using the default event storage location for profile %q: %v", profile, err)
	} else if len(settings.Settings) > 0 && strings.TrimSpace(settings.Settings[0].EventStorageLocation) != "" {
		location = strings.TrimSpace(settings.Settings[0].EventStorageLocation)
	}

	switch {
	case location == "~" || strings.HasPrefix(location, "~/") || strings.HasPrefix(location, `~\`):
		if home, err := os.UserHomeDir(); err == nil {
			location = filepath.Join(home, location[1:])
		}
	case filepath.IsAbs(location):
	case filepath.VolumeName(location) != "":
		location += `\` // a drive on its own, such as "K:"
	case strings.HasPrefix(location, `\`) || strings.HasPrefix(location, "/"):
		location = filepath.VolumeName(tracksideDir) + location // the root of FPVTrackside's drive
	default:
		location = filepath.Join(tracksideDir, location)
	}
	return filepath.Clean(location), profile
}

// eventListing is one event in the Load Event list.
type eventListing struct {
	Label string // its name, with the date or ID added if another has the same one
	Name  string
	ID    string // its folder
}

// listEvents lists the events in folder, newest first by their start date,
// and those on the same day by name. A subfolder without a readable
// Event.json is left out.
func listEvents(folder string) ([]eventListing, error) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil, err
	}
	type listed struct {
		name, id string
		start    time.Time // zero, so last, if it can't be read
	}
	var events []listed
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var event []eventRecord
		if err := readJSONFile(filepath.Join(folder, e.Name(), "Event.json"), &event); err != nil || len(event) == 0 {
			continue
		}
		start, _ := parseTracksideTime(event[0].Start)
		events = append(events, listed{event[0].Name, e.Name(), start})
	}
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		da, db := a.start.Truncate(24*time.Hour), b.start.Truncate(24*time.Hour)
		if !da.Equal(db) {
			return da.After(db)
		}
		if strings.ToLower(a.name) != strings.ToLower(b.name) {
			return strings.ToLower(a.name) < strings.ToLower(b.name)
		}
		return a.start.After(b.start)
	})

	taken := map[string]bool{}
	list := make([]eventListing, 0, len(events))
	for _, ev := range events {
		label := ev.name
		if taken[label] {
			label = fmt.Sprintf("%s (%s)", ev.name, ev.start.Format("2 Jan 2006 15:04"))
		}
		if taken[label] {
			label = fmt.Sprintf("%s (%s)", ev.name, ev.id)
		}
		taken[label] = true
		list = append(list, eventListing{label, ev.name, ev.id})
	}
	return list, nil
}

// --- A loaded event ---

// lapData is a lap that counts: one ended by a valid detection. Number 0 is
// the holeshot.
type lapData struct {
	ID      string
	PilotID string
	Number  int
	Seconds float64
}

// sectorTime is one sector of one lap.
type sectorTime struct {
	LapID     string
	PilotID   string
	LapNumber int
	Sector    int // from 1
	Seconds   float64
}

type raceData struct {
	ID          string
	RoundID     string
	Valid       bool
	Laps        []lapData
	Sectors     []sectorTime
	SectorCount int // sectors in a lap of this race; 0 without split gates
}

// parseRace keeps what counts from a Race.json: the laps ended by a valid
// detection, credited to that detection's pilot, and their sector times.
func parseRace(id string, r raceRecord) *raceData {
	byID := make(map[string]raceDetection, len(r.Detections))
	for _, d := range r.Detections {
		byID[d.ID] = d
	}
	race := &raceData{ID: id, RoundID: r.Round, Valid: r.Valid}
	for _, lap := range r.Laps {
		det, ok := byID[lap.Detection]
		if !ok || !det.Valid {
			continue
		}
		race.Laps = append(race.Laps, lapData{lap.ID, det.Pilot, lap.LapNumber, lap.LengthSeconds})
	}
	race.Sectors, race.SectorCount = lapSectors(r.Laps, r.Detections)
	return race
}

// lapSectors works out the sector times of a race's valid laps from its
// split gates: Trackside timing systems 1 and up. With N split gates a lap has
// N+1 sectors: from the lap's start to split gate 1, on to gate 2 and so on,
// and from the last split gate to the line. A race with no split gate passes
// has none.
//
// The highest split gate anyone passed in the race is taken as the last one.
// Within a lap only the first valid pass of each gate counts, as a timing
// gate can pick a pilot up twice, so a lap's sectors add up to its lap time.
// A sector is left out if a pass at either end of it is missing.
//
// It returns the sector times and how many sectors a lap has.
func lapSectors(laps []raceLap, detections []raceDetection) ([]sectorTime, int) {
	splits := 0
	for _, d := range detections {
		if !d.IsLapEnd && d.TimingSystemIndex > splits {
			splits = d.TimingSystemIndex
		}
	}
	if splits == 0 {
		return nil, 0
	}

	type pass struct {
		gate int
		at   time.Time
	}
	byID := make(map[string]raceDetection, len(detections))
	passes := map[string][]pass{} // by pilot
	for _, d := range detections {
		byID[d.ID] = d
		if !d.Valid || d.IsLapEnd || d.TimingSystemIndex < 1 {
			continue
		}
		at, err := parseTracksideTime(d.Time)
		if err != nil {
			continue
		}
		passes[d.Pilot] = append(passes[d.Pilot], pass{d.TimingSystemIndex, at})
	}

	var sectors []sectorTime
	for _, lap := range laps {
		det, ok := byID[lap.Detection]
		if !ok || !det.Valid || lap.LapNumber < 1 {
			continue // the holeshot isn't a lap with sectors
		}
		start, err1 := parseTracksideTime(lap.StartTime)
		end, err2 := parseTracksideTime(lap.EndTime)
		if err1 != nil || err2 != nil || !end.After(start) {
			continue
		}
		// bounds[g] is when the pilot passed gate g in this lap: 0 the lap's
		// start, splits+1 its end.
		bounds := make([]time.Time, splits+2)
		have := make([]bool, splits+2)
		bounds[0], have[0] = start, true
		bounds[splits+1], have[splits+1] = end, true
		for _, p := range passes[det.Pilot] {
			if p.at.After(start) && p.at.Before(end) && (!have[p.gate] || p.at.Before(bounds[p.gate])) {
				bounds[p.gate], have[p.gate] = p.at, true
			}
		}
		for s := 1; s <= splits+1; s++ {
			if have[s-1] && have[s] && bounds[s].After(bounds[s-1]) {
				sectors = append(sectors, sectorTime{lap.ID, det.Pilot, lap.LapNumber, s, bounds[s].Sub(bounds[s-1]).Seconds()})
			}
		}
	}
	return sectors, splits + 1
}

// eventData is an event as read from its folder, kept up to date by refresh.
type eventData struct {
	dir    string // the event's folder
	ID     string
	Name   string
	pilots map[string]string    // ID -> name
	races  map[string]*raceData // by race folder
	stamps map[string]time.Time // when each file read was last modified
}

func newEventData(eventsDir, id, name string) *eventData {
	return &eventData{
		dir: filepath.Join(eventsDir, id), ID: id, Name: name,
		pilots: map[string]string{},
		races:  map[string]*raceData{}, stamps: map[string]time.Time{},
	}
}

// reload reads a file of the event into v if it has changed since it was last
// read, and reports whether it did. A file Trackside is part-way through
// writing fails to parse; it isn't marked as read, so the next refresh tries
// it again.
func (e *eventData) reload(file string, v any) (bool, error) {
	info, err := os.Stat(filepath.Join(e.dir, file))
	if err != nil {
		return false, err
	}
	if old, ok := e.stamps[file]; ok && !info.ModTime().After(old) {
		return false, nil
	}
	if err := readJSONFile(filepath.Join(e.dir, file), v); err != nil {
		return false, err
	}
	e.stamps[file] = info.ModTime()
	return true, nil
}

// refresh re-reads whatever in the event's folder has changed and reports
// whether anything did. It is what makes the program live: called every few
// seconds while Trackside runs the event.
func (e *eventData) refresh() (changed bool, err error) {
	entries, err := os.ReadDir(e.dir)
	if err != nil {
		return false, err
	}

	var pilots []pilotRecord
	if ok, perr := e.reload("Pilots.json", &pilots); perr != nil {
		err = perr
	} else if ok {
		e.pilots = make(map[string]string, len(pilots))
		for _, p := range pilots {
			e.pilots[p.ID] = p.Name
		}
		changed = true
	}

	seen := map[string]bool{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		var race []raceRecord
		ok, rerr := e.reload(filepath.Join(id, "Race.json"), &race)
		if rerr != nil {
			if os.IsNotExist(rerr) {
				continue // not a race folder
			}
			seen[id] = true // keep what we have until it reads cleanly
			err = rerr
			continue
		}
		seen[id] = true
		if ok && len(race) > 0 {
			e.races[id] = parseRace(id, race[0])
			changed = true
		}
	}
	for id := range e.races {
		if !seen[id] {
			delete(e.races, id)
			delete(e.stamps, filepath.Join(id, "Race.json"))
			changed = true
		}
	}
	return changed, err
}
