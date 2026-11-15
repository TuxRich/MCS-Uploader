package main

import (
	"sort"
	"strconv"
	"strings"
)

// defaultLapLimit is the lap time, in seconds, below which a lap is flagged
// unless the settings say otherwise.
const defaultLapLimit = 9

// quickLapShare decides when one lap of a run is "a lot quicker" than the
// other two: when it took less than this share of the quicker of them, that
// is, when it is over a quarter quicker than both. A missed gate or a false
// detection splitting a lap looks like this; a crash, which makes one lap
// slower, doesn't.
const quickLapShare = 0.75

// lapCheck is what looks wrong about a pilot's 3-lap run.
type lapCheck struct {
	Under     [3]bool // lap 1, 2, 3 is under the limit
	BestUnder bool    // none of those is, but the pilot's best lap elsewhere is
	Quick     int     // 1-3: that lap is a lot quicker than the other two; 0: none is
	limit     float64
}

// checkLaps looks over a run's three laps and the pilot's best lap. A limit
// of 0 turns the lap time check off.
func checkLaps(lap1, lap2, lap3, best, limit float64) lapCheck {
	c := lapCheck{limit: limit}
	laps := [3]float64{lap1, lap2, lap3}
	if limit > 0 {
		anyUnder := false
		for i, l := range laps {
			if l > 0 && l < limit {
				c.Under[i], anyUnder = true, true
			}
		}
		c.BestUnder = !anyUnder && best > 0 && best < limit
	}
	if lap1 > 0 && lap2 > 0 && lap3 > 0 {
		order := []int{0, 1, 2}
		sort.Slice(order, func(i, j int) bool { return laps[order[i]] < laps[order[j]] })
		if laps[order[0]] < quickLapShare*laps[order[1]] {
			c.Quick = order[0] + 1
		}
	}
	return c
}

// Flagged reports whether anything looks wrong.
func (c lapCheck) Flagged() bool {
	return c.Under[0] || c.Under[1] || c.Under[2] || c.BestUnder || c.Quick != 0
}

// Lap reports whether lap n (1-3) is one of the laps that look wrong.
func (c lapCheck) Lap(n int) bool {
	return n >= 1 && n <= 3 && (c.Under[n-1] || c.Quick == n)
}

// String says what looks wrong, "" if nothing does.
func (c lapCheck) String() string {
	limit := strconv.FormatFloat(c.limit, 'f', -1, 64) + "s"
	var parts, under []string
	for i, u := range c.Under {
		if u {
			under = append(under, strconv.Itoa(i+1))
		}
	}
	switch len(under) {
	case 1:
		parts = append(parts, "Lap "+under[0]+" under "+limit)
	case 2:
		parts = append(parts, "Laps "+under[0]+" and "+under[1]+" under "+limit)
	case 3:
		parts = append(parts, "All 3 laps under "+limit)
	}
	if c.BestUnder {
		parts = append(parts, "Best lap under "+limit)
	}
	if c.Quick != 0 {
		parts = append(parts, "Lap "+strconv.Itoa(c.Quick)+" much quicker than the others")
	}
	return strings.Join(parts, "; ")
}

// lapRun is a pilot's best run of consecutive laps in the event.
type lapRun struct {
	Name                   string
	Total                  float64
	Lap1, Lap2, Lap3       float64 // Lap3 is 0 for a 2-lap run
	BestLap                float64 // fastest single lap
	RoundsFlown, LapsFlown int
	RaceID, PilotID        string // where the run was flown
	LastLap                int    // lap number of the run's last lap
}

// countedRaces returns the event's races that count for times: every valid
// one, whatever type of round it is in (time trial, race, practice...).
// Sorted by ID so results don't depend on map order.
func (e *eventData) countedRaces() []*raceData {
	var races []*raceData
	for _, r := range e.races {
		if r.Valid {
			races = append(races, r)
		}
	}
	sort.Slice(races, func(i, j int) bool { return races[i].ID < races[j].ID })
	return races
}

// consecutiveLapTotals returns each pilot's fastest nLaps consecutive laps
// over the event's races, fastest first. Laps are consecutive only when
// flown one after another in the same race; the holeshot (lap 0) is never
// part of a run. Pilots are told apart by name, as MCS does. A pilot without
// nLaps consecutive laps yet is left out.
func consecutiveLapTotals(e *eventData, nLaps int) []lapRun {
	type stats struct {
		run     lapRun
		hasRun  bool
		rounds  map[string]bool
		bestLap float64
		laps    int
	}
	byName := map[string]*stats{}

	for _, race := range e.countedRaces() {
		// Each pilot's laps in this race, by lap number.
		perPilot := map[string]map[int]float64{}
		for _, lap := range race.Laps {
			name, ok := e.pilots[lap.PilotID]
			if !ok {
				continue
			}
			st := byName[name]
			if st == nil {
				st = &stats{rounds: map[string]bool{}}
				st.run.Name = name
				byName[name] = st
			}
			st.rounds[race.RoundID] = true // the holeshot counts as having flown
			if lap.Number < 1 {
				continue
			}
			st.laps++
			if st.bestLap == 0 || lap.Seconds < st.bestLap {
				st.bestLap = lap.Seconds
			}
			if perPilot[lap.PilotID] == nil {
				perPilot[lap.PilotID] = map[int]float64{}
			}
			if _, dup := perPilot[lap.PilotID][lap.Number]; !dup {
				perPilot[lap.PilotID][lap.Number] = lap.Seconds
			}
		}

		pilotIDs := make([]string, 0, len(perPilot))
		for id := range perPilot {
			pilotIDs = append(pilotIDs, id)
		}
		sort.Strings(pilotIDs)
		for _, pilotID := range pilotIDs {
			laps := perPilot[pilotID]
			st := byName[e.pilots[pilotID]]
			numbers := make([]int, 0, len(laps))
			for n := range laps {
				numbers = append(numbers, n)
			}
			sort.Ints(numbers)
			for _, last := range numbers {
				total, complete := 0.0, true
				for n := last - nLaps + 1; n <= last; n++ {
					secs, ok := laps[n]
					if !ok {
						complete = false
						break
					}
					total += secs
				}
				if !complete || (st.hasRun && total >= st.run.Total) {
					continue
				}
				st.hasRun = true
				st.run.Total = total
				st.run.RaceID, st.run.PilotID, st.run.LastLap = race.ID, pilotID, last
				st.run.Lap1, st.run.Lap2, st.run.Lap3 = 0, 0, 0
				first := last - nLaps + 1
				st.run.Lap1 = laps[first]
				if nLaps >= 2 {
					st.run.Lap2 = laps[first+1]
				}
				if nLaps >= 3 {
					st.run.Lap3 = laps[first+2]
				}
			}
		}
	}

	list := make([]lapRun, 0, len(byName))
	for _, st := range byName {
		if !st.hasRun {
			continue
		}
		st.run.BestLap = st.bestLap
		st.run.RoundsFlown = len(st.rounds)
		st.run.LapsFlown = st.laps
		list = append(list, st.run)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Total != list[j].Total {
			return list[i].Total < list[j].Total
		}
		return list[i].Name < list[j].Name
	})
	return list
}

// mcsRow is one pilot's result in an MCS upload. lap1/lap2/lap3 are the three
// laps making up the time, best_lap the pilot's fastest single lap. All are
// optional and must be positive when present, so zero (not recorded) values
// are omitted.
//
// On a track with split gates, lap1_sectors to lap3_sectors are those laps'
// sector times in order, and best_sectors the pilot's fastest time in each
// sector over the event's races. Each is only sent when every sector has
// a time.
type mcsRow struct {
	Pilot       string    `json:"pilot"`
	Time        float64   `json:"time"`
	Lap1        float64   `json:"lap1,omitempty"`
	Lap2        float64   `json:"lap2,omitempty"`
	Lap3        float64   `json:"lap3,omitempty"`
	BestLap     float64   `json:"best_lap,omitempty"`
	Lap1Sectors []float64 `json:"lap1_sectors,omitempty"`
	Lap2Sectors []float64 `json:"lap2_sectors,omitempty"`
	Lap3Sectors []float64 `json:"lap3_sectors,omitempty"`
	BestSectors []float64 `json:"best_sectors,omitempty"`
}

// mcsRows turns the 3-lap leaderboard into MCS upload rows, with the sector
// times the event has.
func mcsRows(e *eventData, list []lapRun) []mcsRow {
	type sectorKey struct {
		race, pilot string
		lap, sector int
	}
	times := map[sectorKey]float64{}
	best := map[string]map[int]float64{} // pilot name -> sector -> fastest
	bestCount := 0
	for _, race := range e.countedRaces() {
		for _, s := range race.Sectors {
			times[sectorKey{race.ID, s.PilotID, s.LapNumber, s.Sector}] = s.Seconds
			name, ok := e.pilots[s.PilotID]
			if !ok {
				continue
			}
			if best[name] == nil {
				best[name] = map[int]float64{}
			}
			if old, ok := best[name][s.Sector]; !ok || s.Seconds < old {
				best[name][s.Sector] = s.Seconds
			}
			bestCount = max(bestCount, s.Sector)
		}
	}
	// complete is a lap's sector times, or nil unless every sector has one.
	complete := func(r lapRun, lap int) []float64 {
		race := e.races[r.RaceID]
		if race == nil || race.SectorCount == 0 {
			return nil
		}
		out := make([]float64, race.SectorCount)
		for s := 1; s <= race.SectorCount; s++ {
			secs, ok := times[sectorKey{r.RaceID, r.PilotID, lap, s}]
			if !ok {
				return nil
			}
			out[s-1] = secs
		}
		return out
	}

	rows := make([]mcsRow, 0, len(list))
	for _, r := range list {
		row := mcsRow{
			Pilot: r.Name, Time: r.Total,
			Lap1: r.Lap1, Lap2: r.Lap2, Lap3: r.Lap3, BestLap: r.BestLap,
			Lap1Sectors: complete(r, r.LastLap-2),
			Lap2Sectors: complete(r, r.LastLap-1),
			Lap3Sectors: complete(r, r.LastLap),
		}
		if b := best[r.Name]; bestCount > 0 && len(b) == bestCount {
			row.BestSectors = make([]float64, bestCount)
			for s := 1; s <= bestCount; s++ {
				row.BestSectors[s-1] = b[s]
			}
		}
		rows = append(rows, row)
	}
	return rows
}
