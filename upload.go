package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Uploading an event's times to MCS.
//
// The first upload of an event sends its times as one upload. Uploading the
// event again doesn't send them afresh: MCS announces every upload it is
// sent, and replacing an upload loses what has been added to its times on the
// site since (DVR links, say). Instead MCS is asked what it has of the event,
// and only the differences are sent, each pilot's on its own:
//
//   - a time MCS has that is different now is changed where it is;
//   - a pilot MCS has no time for is added, in a further upload of the event;
//   - a pilot MCS has a time for, who has no 3 consecutive laps in the event
//     any more, has that time removed.
//
// All of that only for the pilots ticked in the upload window: an unticked
// pilot's time on MCS is left as it is.

// mcsResult is one time of an upload, as MCS has it.
type mcsResult struct {
	ID       int64   `json:"id"`
	Pilot    string  `json:"pilot"`
	Seconds  float64 `json:"seconds"`
	VideoURL string  `json:"video_url"`
	Laps     mcsLaps `json:"laps"`
}

// mcsLaps are the laps MCS has with a time; 0 for a lap it hasn't.
type mcsLaps struct {
	Lap1    float64 `json:"lap1"`
	Lap2    float64 `json:"lap2"`
	Lap3    float64 `json:"lap3"`
	BestLap float64 `json:"best_lap"`
}

// onMCS is what MCS has of an event: the uploads it went up in, and their
// times.
type onMCS struct {
	IDs     []int64 // the event's uploads that are still there
	TrackID int64   // the track they are for
	ClubID  int64   // and the club
	Mixed   bool    // they aren't all for the one track and club
	Results []mcsResult
}

// notFound reports whether err is MCS saying that what was asked for isn't
// there.
func notFound(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// eventOnMCS reads an event's uploads (see settings.uploadIDs) from MCS. One
// that is no longer there, removed on the site say, is left out.
func (a mcsAPI) eventOnMCS(ids []int64) (onMCS, error) {
	var on onMCS
	for _, id := range ids {
		var body struct {
			Upload struct {
				TrackID int64 `json:"track_id"`
				ClubID  int64 `json:"club_id"`
			} `json:"upload"`
			Results []mcsResult `json:"results"`
		}
		err := a.do("GET", fmt.Sprintf("/my/uploads/%d", id), true, nil, &body)
		if notFound(err) {
			continue
		}
		if err != nil {
			return onMCS{}, err
		}
		if len(on.IDs) == 0 {
			on.TrackID, on.ClubID = body.Upload.TrackID, body.Upload.ClubID
		} else if body.Upload.TrackID != on.TrackID || body.Upload.ClubID != on.ClubID {
			on.Mixed = true
		}
		on.IDs = append(on.IDs, id)
		on.Results = append(on.Results, body.Results...)
	}
	return on, nil
}

// elsewhere reports whether the event's times are on MCS for another track or
// club than these, so that uploading the event here means moving them. A
// clubID of 0 is the key's own club, which is the one its uploads are for.
func (o onMCS) elsewhere(trackID, clubID int64) bool {
	return len(o.IDs) > 0 && (o.Mixed || o.TrackID != trackID || (clubID != 0 && o.ClubID != clubID))
}

// uploadLine is one pilot in the upload window: their time in the event, the
// time MCS has for them from the event's upload, or both.
type uploadLine struct {
	Row mcsRow    // their time in the event; Time is 0 if they have no 3 consecutive laps in it now
	On  mcsResult // what MCS has; ID is 0 if it has nothing
}

// uploadDoes is what uploading a line does on MCS.
type uploadDoes int

const (
	doesAdd     uploadDoes = iota // MCS has no time for the pilot: this one goes on
	doesChange                    // MCS has another time, or other laps: they are changed
	doesNothing                   // MCS has just this already
	doesRemove                    // the pilot has no time in the event now: MCS's comes off
)

// sameTime reports whether two times are the same to the millisecond.
func sameTime(a, b float64) bool { return a-b < 0.0005 && b-a < 0.0005 }

func (l uploadLine) sameLaps() bool {
	return sameTime(l.Row.Lap1, l.On.Laps.Lap1) && sameTime(l.Row.Lap2, l.On.Laps.Lap2) &&
		sameTime(l.Row.Lap3, l.On.Laps.Lap3) && sameTime(l.Row.BestLap, l.On.Laps.BestLap)
}

func (l uploadLine) does() uploadDoes {
	switch {
	case l.On.ID == 0:
		return doesAdd
	case l.Row.Time <= 0:
		return doesRemove
	case sameTime(l.Row.Time, l.On.Seconds) && l.sameLaps():
		return doesNothing
	}
	return doesChange
}

// onMCSText is what the upload window shows of the time MCS has: the time,
// and a word when the time is the same but the laps with it aren't, as that
// is a change too. A pilot MCS has no time for is new to it.
func (l uploadLine) onMCSText() string {
	if l.On.ID == 0 {
		return "New"
	}
	text := secs(l.On.Seconds)
	if l.does() == doesChange && sameTime(l.Row.Time, l.On.Seconds) {
		text += " (other laps)"
	}
	return text
}

// removalNote is what the Check column says of a pilot MCS has a time for who
// has no 3 consecutive laps in the event any more.
func removalNote(ticked bool) string {
	if ticked {
		return "No 3 laps now: this time will be removed from MCS"
	}
	return "No 3 laps now. Unticked, so the time stays on MCS"
}

// uploadLines pairs the event's times with what MCS has of the event, by
// pilot's name as MCS itself matches them. The event's pilots come first, in
// the order given; after them the pilots MCS has a time for who have none in
// the event now.
func uploadLines(rows []mcsRow, results []mcsResult) []uploadLine {
	byName := map[string]mcsResult{}
	for _, r := range results {
		if _, taken := byName[nameKey(r.Pilot)]; !taken {
			byName[nameKey(r.Pilot)] = r
		}
	}
	lines := make([]uploadLine, 0, len(rows)+len(byName))
	listed := map[string]bool{}
	for _, row := range rows {
		listed[nameKey(row.Pilot)] = true
		lines = append(lines, uploadLine{Row: row, On: byName[nameKey(row.Pilot)]})
	}
	for _, r := range results {
		if k := nameKey(r.Pilot); !listed[k] {
			listed[k] = true
			lines = append(lines, uploadLine{Row: mcsRow{Pilot: r.Pilot}, On: r})
		}
	}
	return lines
}

// uploadTicks says which lines start ticked when an upload is confirmed: all
// but those of the pilots held back from the event's upload last time (see
// heldBack).
func uploadTicks(lines []uploadLine, skipped []string) []bool {
	out := map[string]bool{}
	for _, name := range skipped {
		out[nameKey(name)] = true
	}
	ticks := make([]bool, len(lines))
	for i, l := range lines {
		ticks[i] = !out[nameKey(l.Row.Pilot)]
	}
	return ticks
}

// heldBack names the pilots whose unticking kept something from MCS: a time
// that would have been added, changed or removed. They start unticked next
// time. Unticking a pilot whose time MCS has just as it is keeps nothing
// back, so it isn't remembered: when their time does change, they are ticked.
func heldBack(lines []uploadLine, ticks []bool) []string {
	var names []string
	for i, l := range lines {
		if !ticks[i] && l.does() != doesNothing {
			names = append(names, l.Row.Pilot)
		}
	}
	return names
}

// uploadPlan is what an upload does on MCS.
type uploadPlan struct {
	Add    []mcsRow     // times MCS hasn't got
	Change []uploadLine // times it has that are different now
	Remove []mcsResult  // its times for pilots who have none now
	Same   int          // ticked times it has just as they are
}

// planUpload works out what the ticked lines come to. Unticked ones come to
// nothing: what MCS has for those pilots stays as it is.
func planUpload(lines []uploadLine, ticks []bool) uploadPlan {
	var p uploadPlan
	for i, l := range lines {
		if !ticks[i] {
			continue
		}
		switch l.does() {
		case doesAdd:
			p.Add = append(p.Add, l.Row)
		case doesChange:
			p.Change = append(p.Change, l)
		case doesRemove:
			p.Remove = append(p.Remove, l.On)
		default:
			p.Same++
		}
	}
	return p
}

// empty reports whether there is nothing to send.
func (p uploadPlan) empty() bool { return len(p.Add)+len(p.Change)+len(p.Remove) == 0 }

// String lists what there is to do: "1 to add, 2 to change".
func (p uploadPlan) String() string {
	var parts []string
	for _, part := range []struct {
		n    int
		what string
	}{{len(p.Add), "to add"}, {len(p.Change), "to change"}, {len(p.Remove), "to remove"}, {p.Same, "already the same on MCS"}} {
		if part.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", part.n, part.what))
		}
	}
	return strings.Join(parts, ", ")
}

// uploadSummary is the line under the upload window's list: how many pilots
// are ticked and, when the upload goes by what MCS already has of the event
// (inPlace), what they come to.
func uploadSummary(lines []uploadLine, ticks []bool, checks []lapCheck, inPlace bool) string {
	ticked, toCheck := 0, 0
	for i, l := range lines {
		if ticks[i] {
			ticked++
			// Only laps about to be sent matter: those of the ticked pilots
			// whose times MCS hasn't got already.
			if d := l.does(); checks[i].Flagged() && (d == doesAdd || d == doesChange) {
				toCheck++
			}
		}
	}
	text := fmt.Sprintf("%d of %d pilots ticked", ticked, len(lines))
	if inPlace && ticked > 0 {
		text += ": " + planUpload(lines, ticks).String()
	}
	if toCheck > 0 {
		text += fmt.Sprintf("; %d with laps to check (see the Check column)", toCheck)
	}
	return text
}

// uploadNote is what the upload window says about the times MCS already has
// of the event. otherTrack is that they are at another track than the one
// chosen now; was is the club they are for and now the club chosen, either
// with ID 0 if there is no saying.
func uploadNote(otherTrack bool, was, now mcsClub) string {
	const moved = "all of those come off MCS, and the ticked pilots' times go on"
	switch {
	case otherTrack:
		return "This event's times are on MCS at another track. Uploading moves them: " + moved + " at this one."
	case was.ID != 0 && now.ID != 0 && was.ID != now.ID:
		from := was.Name
		if from == "" {
			from = "another club"
		}
		return fmt.Sprintf("This event's times are on MCS for %s. Uploading for %s moves them: %s for %s.", from, now.Name, moved, now.Name)
	}
	return "This event is already on MCS, so only what has changed is sent. An unticked pilot's time on MCS is left as it is."
}

// addResults sends times to a track as a new upload, and returns the upload's
// id and how many rows MCS took. clubID is for an admin's key, which has to
// say which club the times are for; 0 otherwise.
func (a mcsAPI) addResults(trackID, clubID int64, rows []mcsRow) (uploadID int64, imported int, err error) {
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

// changeResult changes a time MCS has to what it is in the event now.
func (a mcsAPI) changeResult(l uploadLine) error {
	// Every lap is sent, so that one MCS has and the event hasn't is cleared.
	payload := struct {
		Time    *float64 `json:"time,omitempty"`
		Lap1    float64  `json:"lap1"`
		Lap2    float64  `json:"lap2"`
		Lap3    float64  `json:"lap3"`
		BestLap float64  `json:"best_lap"`
	}{Lap1: l.Row.Lap1, Lap2: l.Row.Lap2, Lap3: l.Row.Lap3, BestLap: l.Row.BestLap}
	// The time only if it has changed: to MCS a time sent is a new time, and
	// an admin's check of the DVR against the old one no longer stands.
	if !sameTime(l.Row.Time, l.On.Seconds) {
		payload.Time = &l.Row.Time
	}
	return a.do("PATCH", fmt.Sprintf("/my/results/%d", l.On.ID), true, payload, nil)
}

// uploadSent is what an upload did on MCS.
type uploadSent struct {
	Changed, Removed, Added int
	NewUpload               int64 // the upload MCS made of the added times; 0 if none were added
}

// send carries out a plan. Changes go first, each on its own, and the new
// times last, all at once. It stops at the first thing MCS refuses and says
// how far it got; sending the same again then finishes the job, as what has
// been done already is by then the same on MCS.
func (a mcsAPI) send(trackID, clubID int64, plan uploadPlan) (sent uploadSent, err error) {
	add := append([]mcsRow(nil), plan.Add...)
	for _, l := range plan.Change {
		err := a.changeResult(l)
		if notFound(err) {
			// Removed from MCS since it was read: it goes on again.
			add = append(add, l.Row)
			continue
		}
		if err != nil {
			return sent, fmt.Errorf("couldn't change %s's time: %w", l.Row.Pilot, err)
		}
		sent.Changed++
	}
	for _, r := range plan.Remove {
		// Already gone is as good as removed.
		if err := a.do("DELETE", fmt.Sprintf("/my/results/%d", r.ID), true, nil, nil); err != nil && !notFound(err) {
			return sent, fmt.Errorf("couldn't remove %s's time: %w", r.Pilot, err)
		}
		sent.Removed++
	}
	if len(add) > 0 {
		id, imported, err := a.addResults(trackID, clubID, add)
		if err != nil {
			return sent, fmt.Errorf("couldn't add the new times: %w", err)
		}
		sent.NewUpload, sent.Added = id, imported
	}
	return sent, nil
}

// String lists what was done: "2 changed, 1 added (upload 41)".
func (s uploadSent) String() string {
	var parts []string
	if s.Changed > 0 {
		parts = append(parts, fmt.Sprintf("%d changed", s.Changed))
	}
	if s.Removed > 0 {
		parts = append(parts, fmt.Sprintf("%d removed", s.Removed))
	}
	if s.Added > 0 {
		parts = append(parts, fmt.Sprintf("%d added (upload %d)", s.Added, s.NewUpload))
	}
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, ", ")
}

// moveUploads puts an event's times on MCS afresh, for a track or club other
// than the one they are there for: its uploads there (old) are deleted, and
// rows sent as a new one. It returns the new upload's id and how many rows MCS
// took.
func (a mcsAPI) moveUploads(old []int64, trackID, clubID int64, rows []mcsRow) (uploadID int64, imported int, err error) {
	for _, id := range old {
		// Already gone (removed on the site, say) is as good as deleted.
		if err := a.do("DELETE", fmt.Sprintf("/my/uploads/%d", id), true, nil, nil); err != nil && !notFound(err) {
			return 0, 0, fmt.Errorf("could not remove previous upload %d: %w", id, err)
		}
	}
	return a.addResults(trackID, clubID, rows)
}
