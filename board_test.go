package main

import "testing"

func testBase() []mcsEntry {
	return []mcsEntry{
		{Rank: 1, Pilot: "Zed", ClubID: 9, Club: "Other", BestSeconds: 50},
		{Rank: 2, Pilot: "Alice", ClubID: 2, Club: "Mine", BestSeconds: 55},
		{Rank: 3, Pilot: "Yan", ClubID: 9, Club: "Other", BestSeconds: 60},
		{Rank: 4, Pilot: "Bob", ClubID: 2, Club: "Mine", BestSeconds: 65},
		{Rank: 5, Pilot: "Cara", ClubID: 9, Club: "Other", BestSeconds: 70}, // another club's Cara
	}
}

func find(t *testing.T, rows []boardRow, pilot, club string) boardRow {
	t.Helper()
	for _, r := range rows {
		if r.Pilot == pilot && r.Club == club {
			return r
		}
	}
	t.Fatalf("%s (%s) not on the board: %+v", pilot, club, rows)
	return boardRow{}
}

func TestMergeBoard(t *testing.T) {
	live := []lapRun{
		{Name: "bob ", Total: 52, BestLap: 16},  // was 4th with 65: up to 2nd
		{Name: "Cara", Total: 58},               // my club's Cara is new
		{Name: "Alice", Total: 59, BestLap: 18}, // slower than her MCS 55
	}
	rows := mergeBoard(testBase(), live, 2, "Mine", false)
	if len(rows) != 6 {
		t.Fatalf("got %d rows, want 6: %+v", len(rows), rows)
	}
	for i, r := range rows {
		if r.Pos != i+1 || (i > 0 && r.Time < rows[i-1].Time) {
			t.Fatalf("not in order at %d: %+v", i, rows)
		}
	}

	bob := find(t, rows, "Bob", "Mine")
	if bob.Pos != 2 || bob.Move != 2 || !bob.Better || !bob.Live || bob.New || bob.Time != 52 || bob.BestLap != 16 {
		t.Errorf("Bob: %+v", bob)
	}
	alice := find(t, rows, "Alice", "Mine")
	if alice.Pos != 3 || alice.Move != -1 || alice.Better || !alice.Live || alice.Time != 55 || alice.BestLap != 18 {
		t.Errorf("Alice keeps her MCS time: %+v", alice)
	}
	cara := find(t, rows, "Cara", "Mine")
	if cara.Pos != 4 || !cara.New || !cara.Better || cara.Move != 0 {
		t.Errorf("new Cara: %+v", cara)
	}
	other := find(t, rows, "Cara", "Other")
	if other.Live || other.Pos != 6 || other.Move != -1 || other.Time != 70 {
		t.Errorf("the other club's Cara is untouched but pushed down: %+v", other)
	}
	if zed := find(t, rows, "Zed", "Other"); zed.Pos != 1 || zed.Move != 0 {
		t.Errorf("Zed: %+v", zed)
	}
}

func TestMergeBoardClubOnly(t *testing.T) {
	live := []lapRun{{Name: "Bob", Total: 52}}
	rows := mergeBoard(testBase(), live, 2, "Mine", true)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want my club's 2: %+v", len(rows), rows)
	}
	// Within the club Bob was 2nd of 2 and is now 1st.
	if rows[0].Pilot != "Bob" || rows[0].Move != 1 || rows[1].Pilot != "Alice" || rows[1].Move != -1 {
		t.Errorf("club board: %+v", rows)
	}
}

func TestMergeBoardUnknownClub(t *testing.T) {
	// Without an API key the club isn't known, so the name alone decides.
	rows := mergeBoard(testBase(), []lapRun{{Name: "Cara", Total: 40}}, 0, "", false)
	if len(rows) != 5 || rows[0].Pilot != "Cara" || rows[0].Move != 4 || rows[0].New {
		t.Errorf("got %+v", rows)
	}
}

func TestEventBoard(t *testing.T) {
	rows := eventBoard([]lapRun{{Name: "A", Total: 10}, {Name: "B", Total: 12}})
	if len(rows) != 2 || rows[1].Pos != 2 || rows[1].Run == nil || rows[1].Run.Name != "B" {
		t.Errorf("got %+v", rows)
	}
}
