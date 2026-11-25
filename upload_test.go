package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// What MCS has of the event in these tests, as upload 7: Alice's time just as
// it is in testRows, another time for Bob, and a time for Dave, who has none
// in the event now.
const upload7 = `{"upload":{"id":7,"track_id":4,"club_id":2,"source":"API import"},"results":[
	{"id":36,"pilot_id":3,"pilot":"Alice","seconds":61.5,"time":"1:01.50","video_url":"https://youtu.be/a","laps":{"lap1":20,"lap2":20.5,"lap3":21,"best_lap":20}},
	{"id":37,"pilot_id":4,"pilot":"Bob","seconds":71,"time":"1:11.00"},
	{"id":38,"pilot_id":6,"pilot":"Dave","seconds":80,"time":"1:20.00","laps":{"lap1":26,"lap2":27,"lap3":27,"best_lap":26}}]}`

// The event now: Alice and Bob as in testRows, and Cara, who is new.
var rowsNow = append(append([]mcsRow(nil), testRows...), mcsRow{Pilot: "Cara", Time: 65, Lap1: 21, Lap2: 22, Lap3: 22, BestLap: 21})

func allTicked(lines []uploadLine) []bool { return uploadTicks(lines, nil) }

// An event's first upload sends its times as one upload, and nothing else.
func TestFirstUpload(t *testing.T) {
	f, api := newFake(t)
	on, err := api.eventOnMCS(nil)
	if err != nil || len(on.IDs) != 0 || on.elsewhere(4, 0) {
		t.Fatalf("an event not uploaded yet: %+v, %v", on, err)
	}
	lines := uploadLines(testRows, on.Results)
	sent, err := api.send(4, 0, planUpload(lines, allTicked(lines)))
	if err != nil || sent != (uploadSent{Added: 2, NewUpload: 41}) {
		t.Fatalf("send: %+v, %v", sent, err)
	}
	if got := strings.Join(f.calls, ", "); got != "POST /api/v1/results" {
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

	// An admin's key says which club.
	if _, err := api.send(4, 2, planUpload(lines, allTicked(lines))); err != nil || f.posted["club_id"] != 2.0 {
		t.Errorf("club_id: %v, %v", f.posted["club_id"], err)
	}
	// What MCS refuses is passed on in its words.
	f.postStatus = 400
	if _, err := api.send(4, 0, planUpload(lines, allTicked(lines))); err == nil || !strings.Contains(err.Error(), "unknown track_id (400)") {
		t.Errorf("err: %v", err)
	}
}

// Uploading an event again sends only what is different from what MCS has,
// each pilot's on its own: no upload is deleted and none sent again.
func TestUploadAgain(t *testing.T) {
	f, api := newFake(t)
	f.uploads["7"] = upload7
	on, err := api.eventOnMCS([]int64{7})
	if err != nil || len(on.IDs) != 1 || on.TrackID != 4 || on.ClubID != 2 || len(on.Results) != 3 || on.Results[0].Laps.Lap3 != 21 {
		t.Fatalf("what MCS has: %+v, %v", on, err)
	}
	f.calls = nil

	lines := uploadLines(rowsNow, on.Results)
	var does []uploadDoes
	for _, l := range lines {
		does = append(does, l.does())
	}
	if len(lines) != 4 || lines[3].Row.Pilot != "Dave" || lines[3].Row.Time != 0 ||
		does[0] != doesNothing || does[1] != doesChange || does[2] != doesAdd || does[3] != doesRemove {
		t.Fatalf("lines: %+v, doing %v", lines, does)
	}
	plan := planUpload(lines, allTicked(lines))
	if plan.String() != "1 to add, 1 to change, 1 to remove, 1 already the same on MCS" || plan.empty() {
		t.Errorf("plan: %s", plan)
	}
	sent, err := api.send(4, 0, plan)
	if err != nil || sent != (uploadSent{Changed: 1, Removed: 1, Added: 1, NewUpload: 41}) || sent.String() != "1 changed, 1 removed, 1 added (upload 41)" {
		t.Fatalf("send: %+v (%s), %v", sent, sent, err)
	}
	if got := strings.Join(f.calls, ", "); got != "PATCH /api/v1/my/results/37, DELETE /api/v1/my/results/38, POST /api/v1/results" {
		t.Errorf("calls: %s", got)
	}
	// Bob's time is changed where it is, laps and all (he has none).
	if c := f.patched["37"]; c["time"] != 70.0 || c["lap1"] != 0.0 || c["best_lap"] != 0.0 || len(c) != 5 {
		t.Errorf("Bob's change: %v", c)
	}
	// Only Cara is new.
	if rows := f.posted["rows"].([]any); len(rows) != 1 || rows[0].(map[string]any)["pilot"] != "Cara" {
		t.Errorf("added: %v", rows)
	}
}

// The other pilots' times stay as they are when only the one that has
// changed is ticked: nothing is removed, though Cara isn't added either.
func TestUploadOnlyTheTicked(t *testing.T) {
	f, api := newFake(t)
	f.uploads["7"] = upload7
	on, _ := api.eventOnMCS([]int64{7})
	f.calls = nil
	lines := uploadLines(rowsNow, on.Results)
	ticks := []bool{false, true, false, false} // Bob alone
	plan := planUpload(lines, ticks)
	if plan.String() != "1 to change" {
		t.Errorf("plan: %s", plan)
	}
	if _, err := api.send(4, 0, plan); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.calls, ", "); got != "PATCH /api/v1/my/results/37" {
		t.Errorf("calls: %s", got)
	}
	// Cara, not added, and Dave, not removed, start unticked next time. Alice
	// doesn't: her time is on MCS as it is, so unticking her kept nothing back.
	if got := heldBack(lines, ticks); strings.Join(got, ",") != "Cara,Dave" {
		t.Errorf("held back: %v", got)
	}

	// Nothing ticked that differs from MCS: nothing to send at all.
	f.calls = nil
	same := planUpload(lines, []bool{true, false, false, false})
	if !same.empty() || same.Same != 1 {
		t.Errorf("plan: %+v", same)
	}
	if sent, err := api.send(4, 0, same); err != nil || sent.String() != "nothing" || len(f.calls) != 0 {
		t.Errorf("sent %+v, %v, calls %v", sent, err, f.calls)
	}
}

// A time that is the same with other laps has its laps changed, and its time
// left unsent: to MCS a time sent is a new time, which undoes an admin's
// check of the DVR.
func TestUploadLapsAlone(t *testing.T) {
	f, api := newFake(t)
	f.uploads["7"] = upload7
	on, _ := api.eventOnMCS([]int64{7})
	alice := testRows[0]
	alice.BestLap = 19.4 // a quicker single lap since, in another run
	lines := uploadLines([]mcsRow{alice}, on.Results[:1])
	if lines[0].does() != doesChange || lines[0].onMCSText() != "61.500 (other laps)" {
		t.Fatalf("line: %v, %q", lines[0].does(), lines[0].onMCSText())
	}
	if _, err := api.send(4, 0, planUpload(lines, allTicked(lines))); err != nil {
		t.Fatal(err)
	}
	c := f.patched["36"]
	if _, has := c["time"]; has || c["best_lap"] != 19.4 || c["lap2"] != 20.5 {
		t.Errorf("Alice's change: %v", c)
	}
	// A time within a millisecond of MCS's is the same time.
	alice = testRows[0]
	alice.Time += 0.0004
	if l := uploadLines([]mcsRow{alice}, on.Results[:1])[0]; l.does() != doesNothing || l.onMCSText() != "61.500" {
		t.Errorf("a hair's difference: %v", l.does())
	}
}

// What MCS no longer has, or won't have changed.
func TestUploadAgainTrouble(t *testing.T) {
	f, api := newFake(t)
	f.uploads["7"] = upload7
	on, _ := api.eventOnMCS([]int64{7})
	lines := uploadLines(rowsNow, on.Results)
	plan := planUpload(lines, allTicked(lines))

	// Bob's time and Dave's have been removed on the site meanwhile: Bob's
	// goes on again with the new ones, and Dave's being gone is what was wanted.
	f.calls, f.resultStatus["37"], f.resultStatus["38"] = nil, 404, 404
	sent, err := api.send(4, 0, plan)
	if err != nil || sent != (uploadSent{Removed: 1, Added: 2, NewUpload: 41}) {
		t.Errorf("send: %+v, %v", sent, err)
	}
	if rows := f.posted["rows"].([]any); len(rows) != 2 || rows[1].(map[string]any)["pilot"] != "Bob" {
		t.Errorf("added: %v", rows)
	}

	// A change MCS refuses stops the upload there, saying whose.
	f.calls, f.resultStatus["37"] = nil, 403
	_, err = api.send(4, 0, plan)
	if err == nil || !strings.Contains(err.Error(), "Bob") || !strings.Contains(err.Error(), "belongs to another club (403)") {
		t.Errorf("err: %v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("carried on after a refusal: %v", f.calls)
	}
	// As does a removal: what was done before it is reported.
	f.calls, f.resultStatus["37"], f.resultStatus["38"] = nil, 200, 403
	sent, err = api.send(4, 0, plan)
	if err == nil || !strings.Contains(err.Error(), "Dave") || sent != (uploadSent{Changed: 1}) || len(f.calls) != 2 {
		t.Errorf("sent %+v, %v, calls %v", sent, err, f.calls)
	}
}

// An event's uploads as MCS has them: gone ones left out, and whether they
// are for the track and club being uploaded to.
func TestEventOnMCS(t *testing.T) {
	f, api := newFake(t)
	f.uploads["7"] = upload7
	f.uploads["8"] = `{"upload":{"id":8,"track_id":4,"club_id":2},"results":[{"id":50,"pilot":"Eve","seconds":66}]}`
	f.uploads["9"] = `{"upload":{"id":9,"track_id":5,"club_id":2},"results":[]}`

	on, err := api.eventOnMCS([]int64{7, 404, 8})
	if err != nil || len(on.IDs) != 2 || on.IDs[1] != 8 || len(on.Results) != 4 || on.Mixed {
		t.Fatalf("two uploads and one that has gone: %+v, %v", on, err)
	}
	for _, c := range []struct {
		track, club int64
		want        bool
	}{{4, 0, false}, {4, 2, false}, {5, 0, true}, {4, 9, true}} {
		if got := on.elsewhere(c.track, c.club); got != c.want {
			t.Errorf("elsewhere(%d, %d) = %v", c.track, c.club, got)
		}
	}
	// All gone: as if never uploaded.
	if on, err := api.eventOnMCS([]int64{404}); err != nil || len(on.IDs) != 0 || on.elsewhere(4, 0) {
		t.Errorf("gone: %+v, %v", on, err)
	}
	// Uploads that aren't all for one track are elsewhere, wherever that is.
	if on, _ := api.eventOnMCS([]int64{7, 9}); !on.Mixed || !on.elsewhere(4, 0) {
		t.Errorf("mixed: %+v", on)
	}
	// One that can't be read stops it: there is no saying what MCS has.
	if _, err := api.eventOnMCS([]int64{7, 403}); err == nil || !strings.Contains(err.Error(), "another club") {
		t.Errorf("unreadable: %v", err)
	}
}

// Moving an event's times to another track or club replaces its uploads.
func TestMoveUploads(t *testing.T) {
	f, api := newFake(t)
	id, imported, err := api.moveUploads([]int64{7, 8}, 4, 9, testRows)
	if err != nil || id != 41 || imported != 2 {
		t.Fatalf("move: id=%d imported=%d err=%v", id, imported, err)
	}
	if got := strings.Join(f.calls, ", "); got != "DELETE /api/v1/my/uploads/7, DELETE /api/v1/my/uploads/8, POST /api/v1/results" {
		t.Errorf("calls: %s", got)
	}
	if f.posted["club_id"] != 9.0 {
		t.Errorf("club_id: %v", f.posted["club_id"])
	}
	// An upload that is already gone doesn't stop it; one that can't be
	// removed does, before anything is sent.
	f.deleteStatus = 404
	if id, _, err := api.moveUploads([]int64{7}, 4, 0, testRows); err != nil || id != 41 {
		t.Errorf("already gone: id=%d err=%v", id, err)
	}
	f.calls, f.deleteStatus = nil, 403
	if _, _, err := api.moveUploads([]int64{7}, 4, 0, testRows); err == nil || !strings.Contains(err.Error(), "belongs to another club") || len(f.calls) != 1 {
		t.Errorf("err: %v, calls %v", err, f.calls)
	}
}

func TestUploadLines(t *testing.T) {
	results := []mcsResult{
		{ID: 1, Pilot: "ALICE ", Seconds: 60},
		{ID: 2, Pilot: "Dave", Seconds: 80},
		{ID: 3, Pilot: "alice", Seconds: 59}, // a second time for her, in a later upload: left alone
		{ID: 4, Pilot: "Erin", Seconds: 81},
	}
	rows := []mcsRow{{Pilot: "Alice", Time: 60}, {Pilot: "Bob", Time: 61}}
	lines := uploadLines(rows, results)
	if len(lines) != 4 || lines[0].On.ID != 1 || lines[1].On.ID != 0 || lines[2].On.ID != 2 || lines[3].On.ID != 4 {
		t.Fatalf("lines: %+v", lines)
	}
	if lines[2].Row.Pilot != "Dave" || lines[2].onMCSText() != "80.000" || lines[1].onMCSText() != "New" {
		t.Errorf("Dave's line: %+v", lines[2])
	}
	// Nothing on MCS: the event's times, each to add.
	if fresh := uploadLines(rows, nil); len(fresh) != 2 || fresh[0].does() != doesAdd {
		t.Errorf("first upload: %+v", fresh)
	}

	// Bob was unticked last time; so was Erin, whose time would come off.
	ticks := uploadTicks(lines, []string{" bob", "ERIN", "Zed"})
	if !ticks[0] || ticks[1] || !ticks[2] || ticks[3] {
		t.Fatalf("ticks: %v", ticks)
	}
	plan := planUpload(lines, ticks)
	if len(plan.Remove) != 1 || plan.Remove[0].ID != 2 || len(plan.Add) != 0 || plan.Same != 1 {
		t.Errorf("plan: %+v", plan)
	}
	if got := heldBack(lines, ticks); len(got) != 2 || got[0] != "Bob" || got[1] != "Erin" {
		t.Errorf("held back: %v", got)
	}

	// The note on a pilot whose time would come off goes by the tick.
	if on, off := removalNote(true), removalNote(false); !strings.Contains(on, "will be removed from MCS") || !strings.Contains(off, "stays on MCS") {
		t.Errorf("notes: %q, %q", on, off)
	}
}

func TestUploadSummary(t *testing.T) {
	lines := uploadLines(rowsNow, []mcsResult{
		{ID: 36, Pilot: "Alice", Seconds: 61.5, Laps: mcsLaps{20, 20.5, 21, 20}},
		{ID: 37, Pilot: "Bob", Seconds: 71},
		{ID: 38, Pilot: "Dave", Seconds: 80},
	})
	checks := make([]lapCheck, len(lines))
	checks[1] = checkLaps(8, 20, 21, 8, 9) // Bob has a lap to check
	checks[0] = checks[1]                  // as has Alice, but hers are on MCS already: nothing to check now
	all := allTicked(lines)
	for _, c := range []struct {
		ticks   []bool
		inPlace bool
		want    string
	}{
		{all, true, "4 of 4 pilots ticked: 1 to add, 1 to change, 1 to remove, 1 already the same on MCS; 1 with laps to check (see the Check column)"},
		{[]bool{true, false, false, false}, true, "1 of 4 pilots ticked: 1 already the same on MCS"},
		{[]bool{false, false, false, false}, true, "0 of 4 pilots ticked"},
		// A first upload, or a move: there is nothing on MCS to go by.
		{all, false, "4 of 4 pilots ticked; 1 with laps to check (see the Check column)"},
		{[]bool{true, false, true, false}, false, "2 of 4 pilots ticked"},
	} {
		if got := uploadSummary(lines, c.ticks, checks, c.inPlace); got != c.want {
			t.Errorf("ticks %v, in place %v:\n got %q\nwant %q", c.ticks, c.inPlace, got, c.want)
		}
	}
}

func TestUploadNote(t *testing.T) {
	mine, other := mcsClub{2, "Mine"}, mcsClub{9, "Other"}
	for _, c := range []struct{ was, now mcsClub }{{mine, mine}, {mcsClub{}, mine}, {mine, mcsClub{}}, {mcsClub{}, mcsClub{}}} {
		if got := uploadNote(false, c.was, c.now); !strings.Contains(got, "only what has changed is sent") || !strings.Contains(got, "unticked pilot's time on MCS is left as it is") {
			t.Errorf("%+v: %q", c, got)
		}
	}
	// Uploading an event again for another club, or to another track, moves
	// its times.
	if got := uploadNote(false, mine, other); !strings.Contains(got, "on MCS for Mine") || !strings.Contains(got, "for Other moves them") {
		t.Errorf("another club: %q", got)
	}
	if got := uploadNote(false, mcsClub{ID: 2}, other); !strings.Contains(got, "on MCS for another club") {
		t.Errorf("another club, name unknown: %q", got)
	}
	if got := uploadNote(true, mine, mine); !strings.Contains(got, "at another track") || !strings.Contains(got, "moves them") {
		t.Errorf("another track: %q", got)
	}
}

// An event's uploads are remembered: the first where it always was, so that
// older settings still hold, and the later ones beside it.
func TestUploadIDs(t *testing.T) {
	file := filepath.Join(t.TempDir(), "mcs.json")
	s := loadSettings(file)
	if got := s.uploadIDs("ev"); len(got) != 0 {
		t.Fatalf("not uploaded: %v", got)
	}
	s.Uploads["old"] = 7 // as versions before this one left it
	if got := s.uploadIDs("old"); len(got) != 1 || got[0] != 7 {
		t.Errorf("older settings: %v", got)
	}
	s.setUploadIDs("ev", []int64{41, 44, 47})
	if err := s.save(file); err != nil {
		t.Fatal(err)
	}
	got := loadSettings(file)
	if ids := got.uploadIDs("ev"); len(ids) != 3 || ids[0] != 41 || ids[2] != 47 || got.Uploads["ev"] != 41 {
		t.Errorf("reloaded: %v, %+v", ids, got.Uploads)
	}
	got.setUploadIDs("ev", []int64{50})
	if ids := got.uploadIDs("ev"); len(ids) != 1 || ids[0] != 50 || len(got.MoreUploads) != 0 {
		t.Errorf("moved: %v, %+v", ids, got.MoreUploads)
	}
	got.setUploadIDs("ev", nil)
	if _, has := got.Uploads["ev"]; has || len(got.uploadIDs("ev")) != 0 || got.Uploads["old"] != 7 {
		t.Errorf("none: %+v", got.Uploads)
	}
}
