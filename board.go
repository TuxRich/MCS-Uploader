package main

import (
	"sort"
	"strings"
)

// boardRow is one line of the times table.
type boardRow struct {
	Pos     int
	Pilot   string
	Club    string
	Time    float64
	BestLap float64
	Run     *lapRun  // the pilot's run in this event, if they have one
	Live    bool     // flying in this event
	Better  bool     // their time in this event beats what MCS had (or MCS had none)
	New     bool     // not on the MCS board before this event
	Move    int      // places gained (+) or lost (-) against the MCS board
	Check   lapCheck // what looks wrong about the pilot's run in this event
}

// eventBoard is the table for an event on its own: its pilots, fastest first.
func eventBoard(live []lapRun) []boardRow {
	rows := make([]boardRow, len(live))
	for i := range live {
		r := &live[i]
		rows[i] = boardRow{Pos: i + 1, Pilot: r.Name, Time: r.Total, BestLap: r.BestLap, Run: r, Live: true}
	}
	return rows
}

func nameKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// mergeBoard puts an event's times into a track's MCS leaderboard and works
// out how far each pilot has moved.
//
// base is the board as downloaded. With clubOnly it is cut down to myClub
// first, and places are counted within the club. A pilot of the event is the
// same pilot as a board entry when the names match, ignoring case, and the
// entry is myClub's: the rule MCS itself uses when results are uploaded. If
// myClub isn't known (0), the name alone decides.
//
// A pilot keeps the better of their MCS time and their time in the event.
// Move is their place on the MCS board less their place now, so everyone a
// pilot overtakes shows the place they lost.
func mergeBoard(base []mcsEntry, live []lapRun, myClub int64, myClubName string, clubOnly bool) []boardRow {
	entries := make([]mcsEntry, 0, len(base))
	for _, e := range base {
		if clubOnly && e.ClubID != myClub {
			continue
		}
		entries = append(entries, e)
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].BestSeconds < entries[j].BestSeconds })

	rows := make([]boardRow, 0, len(entries)+len(live))
	was := make([]int, 0, cap(rows)) // each row's place on the MCS board; 0 = wasn't on it
	byName := map[string]int{}       // name -> index in rows, for entries the event's pilots can match
	for i, e := range entries {
		row := boardRow{Pilot: e.Pilot, Club: e.Club, Time: e.BestSeconds}
		if e.Laps != nil {
			row.BestLap = e.Laps.BestLap
		}
		rows = append(rows, row)
		was = append(was, i+1)
		if myClub == 0 || e.ClubID == myClub {
			if _, taken := byName[nameKey(e.Pilot)]; !taken {
				byName[nameKey(e.Pilot)] = i
			}
		}
	}

	for i := range live {
		r := &live[i]
		idx, found := byName[nameKey(r.Name)]
		if !found {
			rows = append(rows, boardRow{
				Pilot: r.Name, Club: myClubName, Time: r.Total, BestLap: r.BestLap,
				Run: r, Live: true, Better: true, New: true,
			})
			was = append(was, 0)
			continue
		}
		row := &rows[idx]
		row.Live, row.Run = true, r
		if r.Total < row.Time {
			row.Time, row.Better = r.Total, true
		}
		if r.BestLap > 0 && (row.BestLap == 0 || r.BestLap < row.BestLap) {
			row.BestLap = r.BestLap
		}
	}

	order := make([]int, len(rows))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return rows[order[i]].Time < rows[order[j]].Time })
	out := make([]boardRow, len(rows))
	for pos, idx := range order {
		row := rows[idx]
		row.Pos = pos + 1
		if was[idx] != 0 {
			row.Move = was[idx] - row.Pos
		}
		out[pos] = row
	}
	return out
}
