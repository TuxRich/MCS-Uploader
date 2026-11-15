package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// lapSpec is a lap for a test race: who, which lap, how long, and whether the
// detection that ended it is valid.
type lapSpec struct {
	pilot   string
	number  int
	seconds float64
	valid   bool
}

func testRace(id, round string, valid bool, laps ...lapSpec) *raceData {
	rec := raceRecord{Round: round, Valid: valid, ID: id}
	for i, l := range laps {
		det := id + "-d" + string(rune('a'+i))
		rec.Detections = append(rec.Detections, raceDetection{ID: det, Pilot: l.pilot, Valid: l.valid, IsLapEnd: true})
		rec.Laps = append(rec.Laps, raceLap{ID: id + "-l" + string(rune('a'+i)), Detection: det, LapNumber: l.number, LengthSeconds: l.seconds})
	}
	return parseRace(id, rec)
}

func testEvent(races ...*raceData) *eventData {
	e := newEventData("", "ev", "Test")
	e.pilots = map[string]string{"p1": "Alice", "p2": "Bob", "p3": "Cara"}
	for _, r := range races {
		e.races[r.ID] = r
	}
	return e
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestConsecutiveLapTotals(t *testing.T) {
	e := testEvent(
		// Alice: holeshot then 4 laps; best three in a row are laps 2-4.
		testRace("r1", "tt1", true,
			lapSpec{"p1", 0, 3, true}, lapSpec{"p1", 1, 30, true}, lapSpec{"p1", 2, 20, true},
			lapSpec{"p1", 3, 21, true}, lapSpec{"p1", 4, 22, true},
			// Bob: lap 2 was ended by an invalid detection, so 1 and 3 aren't consecutive.
			lapSpec{"p2", 0, 3, true}, lapSpec{"p2", 1, 10, true}, lapSpec{"p2", 2, 10, false}, lapSpec{"p2", 3, 10, true},
		),
		// Bob's three laps in a second round.
		testRace("r2", "tt2", true,
			lapSpec{"p2", 0, 3, true}, lapSpec{"p2", 1, 25, true}, lapSpec{"p2", 2, 26, true}, lapSpec{"p2", 3, 24, true},
			// Cara: two laps here and one in r1's round don't join up across races.
			lapSpec{"p3", 1, 5, true}, lapSpec{"p3", 2, 5, true},
		),
		testRace("r3", "tt1", true, lapSpec{"p3", 1, 5, true}),
		// Alice is faster still in a race round: any type of round counts.
		testRace("r4", "race1", true, lapSpec{"p1", 1, 19, true}, lapSpec{"p1", 2, 20, true}, lapSpec{"p1", 3, 21, true}),
		// A fast run in an invalid race doesn't.
		testRace("r5", "tt1", false, lapSpec{"p2", 1, 1, true}, lapSpec{"p2", 2, 1, true}, lapSpec{"p2", 3, 1, true}),
	)

	got := consecutiveLapTotals(e, 3)
	if len(got) != 2 {
		t.Fatalf("got %d pilots, want 2 (Cara has no 3 consecutive laps): %+v", len(got), got)
	}
	a, b := got[0], got[1]
	if a.Name != "Alice" || !near(a.Total, 60) || a.Lap1 != 19 || a.Lap2 != 20 || a.Lap3 != 21 || a.LastLap != 3 || a.RaceID != "r4" {
		t.Errorf("Alice's best is from the race round: %+v", a)
	}
	if a.BestLap != 19 || a.RoundsFlown != 2 || a.LapsFlown != 7 {
		t.Errorf("Alice stats: %+v", a)
	}
	if b.Name != "Bob" || !near(b.Total, 75) || b.RaceID != "r2" {
		t.Errorf("Bob: %+v", b)
	}
	// Bob's best lap is from r1 (10s), and he flew in two rounds.
	if b.BestLap != 10 || b.RoundsFlown != 2 || b.LapsFlown != 5 {
		t.Errorf("Bob stats: %+v", b)
	}

	if two := consecutiveLapTotals(e, 2); len(two) != 3 || two[0].Name != "Cara" || !near(two[0].Total, 10) || two[0].Lap3 != 0 {
		t.Errorf("2 laps: %+v", two)
	}
}

func TestCheckLaps(t *testing.T) {
	for _, tc := range []struct {
		l1, l2, l3, best, limit float64
		want                    string
		laps                    [3]bool
	}{
		{12, 12.3, 11.8, 11.8, 9, "", [3]bool{}},                                // an ordinary run
		{12, 8.9, 11.8, 8.9, 9, "Lap 2 under 9s", [3]bool{false, true, false}},  // just under
		{12, 9, 11.8, 9, 9, "", [3]bool{}},                                      // exactly 9 isn't under 9
		{8, 8.5, 12, 8, 9, "Laps 1 and 2 under 9s", [3]bool{true, true, false}}, //
		{5, 5.2, 5.1, 5, 9, "All 3 laps under 9s", [3]bool{true, true, true}},   //
		{12, 12.3, 11.8, 6, 9, "Best lap under 9s", [3]bool{}},                  // a short lap outside the run
		{12, 12.3, 6, 6, 9, "Lap 3 under 9s; Lap 3 much quicker than the others", [3]bool{false, false, true}},
		{20, 14, 20.5, 14, 9, "Lap 2 much quicker than the others", [3]bool{false, true, false}}, // 30% quicker than both
		{20, 16, 20.5, 16, 9, "", [3]bool{}},                                                     // 20% quicker isn't "a lot"
		{12, 12.3, 25, 12, 9, "", [3]bool{}},                                                     // one slow lap (a crash) is fine
		{12, 14, 25, 12, 9, "", [3]bool{}},                                                       //
		{5, 5.2, 5.1, 5, 0, "", [3]bool{}},                                                       // limit off
		{5, 5.2, 3, 3, 0, "Lap 3 much quicker than the others", [3]bool{false, false, true}},     // limit off, the other check still on
		{8, 8.2, 8.1, 8, 8.5, "All 3 laps under 8.5s", [3]bool{true, true, true}},                // a limit that isn't whole
		{20, 14, 0, 14, 9, "", [3]bool{}},                                                        // a 2-lap run has no "other two"
	} {
		c := checkLaps(tc.l1, tc.l2, tc.l3, tc.best, tc.limit)
		if c.String() != tc.want || c.Flagged() != (tc.want != "") {
			t.Errorf("laps %v %v %v best %v limit %v: %q flagged=%v, want %q", tc.l1, tc.l2, tc.l3, tc.best, tc.limit, c.String(), c.Flagged(), tc.want)
		}
		if got := [3]bool{c.Lap(1), c.Lap(2), c.Lap(3)}; got != tc.laps {
			t.Errorf("laps %v %v %v: laps marked %v, want %v", tc.l1, tc.l2, tc.l3, got, tc.laps)
		}
	}
}

func TestSameNameIsOnePilot(t *testing.T) {
	e := testEvent(
		testRace("r1", "tt1", true, lapSpec{"p1", 1, 20, true}, lapSpec{"p1", 2, 20, true}, lapSpec{"p1", 3, 20, true}),
		testRace("r2", "tt2", true, lapSpec{"p2", 1, 15, true}, lapSpec{"p2", 2, 15, true}, lapSpec{"p2", 3, 15, true}),
	)
	e.pilots["p2"] = "Alice"
	got := consecutiveLapTotals(e, 3)
	if len(got) != 1 || !near(got[0].Total, 45) || got[0].PilotID != "p2" || got[0].RoundsFlown != 2 {
		t.Errorf("got %+v", got)
	}
}

func TestSectors(t *testing.T) {
	const day = "2026/11/14 "
	rec := raceRecord{Round: "tt1", Valid: true, ID: "r1"}
	// One split gate, so two sectors a lap. Lap 2 has no split pass.
	ends := []string{"10:00:00", "10:00:10", "10:00:30", "10:00:50"}
	for n := 1; n <= 3; n++ {
		det := "end" + ends[n]
		rec.Detections = append(rec.Detections, raceDetection{ID: det, Pilot: "p1", Valid: true, IsLapEnd: true, Time: day + ends[n]})
		rec.Laps = append(rec.Laps, raceLap{ID: "lap" + ends[n], Detection: det, LapNumber: n, LengthSeconds: []float64{0, 10, 20, 20}[n], StartTime: day + ends[n-1], EndTime: day + ends[n]})
	}
	rec.Detections = append(rec.Detections,
		raceDetection{ID: "s1", Pilot: "p1", Valid: true, TimingSystemIndex: 1, Time: day + "10:00:04"},
		raceDetection{ID: "s1b", Pilot: "p1", Valid: true, TimingSystemIndex: 1, Time: day + "10:00:05"}, // picked up twice
		raceDetection{ID: "s3", Pilot: "p1", Valid: true, TimingSystemIndex: 1, Time: day + "10:00:37"},
	)
	e := testEvent(parseRace("r1", rec))

	rows := mcsRows(e, consecutiveLapTotals(e, 3))
	if len(rows) != 1 {
		t.Fatalf("rows: %+v", rows)
	}
	r := rows[0]
	if len(r.Lap1Sectors) != 2 || !near(r.Lap1Sectors[0], 4) || !near(r.Lap1Sectors[1], 6) {
		t.Errorf("lap 1 sectors: %v", r.Lap1Sectors)
	}
	if r.Lap2Sectors != nil {
		t.Errorf("lap 2 has no split pass, so no sectors: %v", r.Lap2Sectors)
	}
	if len(r.Lap3Sectors) != 2 || !near(r.Lap3Sectors[0], 7) || !near(r.Lap3Sectors[1], 13) {
		t.Errorf("lap 3 sectors: %v", r.Lap3Sectors)
	}
	if len(r.BestSectors) != 2 || !near(r.BestSectors[0], 4) || !near(r.BestSectors[1], 6) {
		t.Errorf("best sectors: %v", r.BestSectors)
	}
}

func TestRefreshFollowsFolder(t *testing.T) {
	dir := t.TempDir()
	write := func(file, text string) {
		t.Helper()
		full := filepath.Join(dir, "ev", file)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("Event.json", `[{"Name":"Night race","Start":"2026/11/14 19:00:00","ID":"ev"}]`)
	write("Pilots.json", "\xef\xbb\xbf"+`[{"ID":"p1","Name":"Alice"}]`)
	race := func(valid string) string {
		return `[{"ID":"r1","Round":"tt1","Valid":` + valid + `,
			"Detections":[{"ID":"d1","Pilot":"p1","Valid":true,"IsLapEnd":true},{"ID":"d2","Pilot":"p1","Valid":true,"IsLapEnd":true},{"ID":"d3","Pilot":"p1","Valid":true,"IsLapEnd":true}],
			"Laps":[{"ID":"l1","Detection":"d1","LapNumber":1,"LengthSeconds":20.5},{"ID":"l2","Detection":"d2","LapNumber":2,"LengthSeconds":20.5},{"ID":"l3","Detection":"d3","LapNumber":3,"LengthSeconds":20.5}]}]`
	}
	write("r1/Race.json", race("true"))

	list, err := listEvents(dir)
	if err != nil || len(list) != 1 || list[0].Label != "Night race" || list[0].ID != "ev" {
		t.Fatalf("listEvents: %+v, %v", list, err)
	}

	e := newEventData(dir, "ev", "Night race")
	if changed, err := e.refresh(); !changed || err != nil {
		t.Fatalf("first refresh: changed=%v err=%v", changed, err)
	}
	if got := consecutiveLapTotals(e, 3); len(got) != 1 || !near(got[0].Total, 61.5) {
		t.Fatalf("after first refresh: %+v", got)
	}
	if changed, _ := e.refresh(); changed {
		t.Error("nothing changed, but refresh says it did")
	}

	// Trackside marks the race invalid: its times go.
	delete(e.stamps, filepath.Join("r1", "Race.json")) // as if the file's time had moved on
	write("r1/Race.json", race("false"))
	if changed, _ := e.refresh(); !changed {
		t.Error("race changed, but refresh says nothing did")
	}
	if got := consecutiveLapTotals(e, 3); len(got) != 0 {
		t.Errorf("invalid race still counted: %+v", got)
	}

	// The race folder is deleted.
	os.RemoveAll(filepath.Join(dir, "ev", "r1"))
	if changed, _ := e.refresh(); !changed || len(e.races) != 0 {
		t.Errorf("deleted race still held: changed=%v races=%d", changed, len(e.races))
	}
}
