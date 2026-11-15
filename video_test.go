package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := parseTracksideTime(s)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestRecordTimesMatchRaceTimes(t *testing.T) {
	// Race.json has local times with no zone; a recording's carry their zone.
	// The same moment has to come out the same from both, summer or winter.
	race := mustTime(t, "2026/05/03 10:24:25.096")
	for _, s := range []string{
		"2026-05-03T10:24:25.096+01:00",     // summer time
		"2026-05-03T10:24:25.0960000+00:00", // had the clocks not changed
		"2026-05-03T10:24:25.096",           // no zone at all
	} {
		got, err := parseRecordTime(s)
		if err != nil || !got.Equal(race) {
			t.Errorf("%s: got %v (%v), want %v", s, got, err, race)
		}
	}
	// Trackside writes hours without a leading zero and drops trailing zeros.
	if got := mustTime(t, "2026/11/08 9:05:01.5"); got.Hour() != 9 || got.Nanosecond() != 500e6 {
		t.Errorf("short time read as %v", got)
	}
	if _, err := parseRecordTime("nonsense"); err == nil {
		t.Error("nonsense read as a time")
	}
}

func TestPosition(t *testing.T) {
	t0 := mustTime(t, "2026/11/08 14:29:12")
	rec := recording{Anchors: []frameAnchor{
		{t0, 0}, {t0.Add(4 * time.Second), 4.2}, {t0.Add(8 * time.Second), 8.4},
	}}
	for _, tc := range []struct {
		after time.Duration
		want  float64
	}{
		{0, 0},
		{1 * time.Second, 1},     // nearest is the first: 0 + 1
		{3 * time.Second, 3.2},   // nearest is the second: 4.2 - 1
		{8 * time.Second, 8.4},   //
		{10 * time.Second, 10.4}, // runs on past the last
		{-2 * time.Second, -2},   // before the recording began
	} {
		if got := rec.position(t0.Add(tc.after)); !near(got, tc.want) {
			t.Errorf("%v after the start: %v, want %v", tc.after, got, tc.want)
		}
	}

	// A source's delay is added, as the video shows things late; and positions
	// count from the first frame even if the recorder's clock didn't start at 0.
	late := recording{Latency: 250 * time.Millisecond, Anchors: []frameAnchor{{t0, 100}, {t0.Add(4 * time.Second), 104}}}
	if got := late.position(t0.Add(5 * time.Second)); !near(got, 5.25) {
		t.Errorf("with a delay and an offset clock: %v, want 5.25", got)
	}

	if !rec.covers(t0, t0.Add(15*time.Second)) || rec.covers(t0.Add(-5*time.Second), t0.Add(5*time.Second)) || rec.covers(t0, t0.Add(30*time.Second)) {
		t.Error("covers is wrong about what the recording has")
	}
}

func TestChannelNames(t *testing.T) {
	for id, want := range map[string]string{
		"00000009-0000-0000-0000-000000000000": "raceband 1",
		"0000000f-0000-0000-0000-000000000000": "raceband 7",
		"00000004-0000-0000-0000-000000000000": "fatshark 4",
		"00000013-0000-0000-0000-000000000000": "a 3",
		"00000031-0000-0000-0000-000000000000": "hdzero r1,sharkbyte r1",
		"00000039-0000-0000-0000-000000000000": "hdzero f2,sharkbyte f2",
		"00000054-0000-0000-0000-000000000000": "hdzero l3,sharkbyte l3",
		"0000004b-0000-0000-0000-000000000000": "djio3 o1",
		"00000064-0000-0000-0000-000000000000": "djio4 r1",
		"00000075-0000-0000-0000-000000000000": "walksnail r8",
		"00000000-0000-0000-0000-000000000000": "",
		"0000005a-0000-0000-0000-000000000000": "", // no such channel
		"not a guid":                           "",
	} {
		if got := strings.Join(channelNames(id), ","); got != want {
			t.Errorf("%s: %q, want %q", id, got, want)
		}
	}
}

func TestClipNamesAndArgs(t *testing.T) {
	if got := clipFileName("Track #3 Heinz sight", "King Joshy", 24.617); got != "MCS - Track #3 Heinz sight - King Joshy - 24.617.mp4" {
		t.Errorf("name: %q", got)
	}
	// What Windows won't have in a file name goes.
	if got := clipFileName(`A/B: "fast" <1>`, "Dot. ", 61.5); got != "MCS - A_B_ _fast_ _1_ - Dot. - 61.500.mp4" {
		t.Errorf("awkward name: %q", got)
	}
	if got := cleanFileName("ends with dots..."); got != "ends with dots" {
		t.Errorf("trailing dots: %q", got)
	}

	p := clipPlan{Video: `C:\ev\race\0.mp4`, Start: 1795.3912, Length: 46.58, HasCrop: true, Crop: relRect{0.5625, 0, 0.375, 0.5}}
	args := strings.Join(clipArgs(p, "out.part", true), " ")
	for _, want := range []string{
		`-ss 1795.391 -t 46.580 -i C:\ev\race\0.mp4`, // seeks before reading, so it is quick an hour in
		"-vf crop=2*round(iw*0.937500/2)-2*round(iw*0.562500/2):2*round(ih*0.500000/2)-2*round(ih*0.000000/2):2*round(iw*0.562500/2):2*round(ih*0.000000/2)",
		"-c:v libx264", "-f mp4 out.part",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("arguments lack %q:\n%s", want, args)
		}
	}
	if whole := strings.Join(clipArgs(p, "out.part", false), " "); strings.Contains(whole, "-vf") {
		t.Errorf("whole picture asked for, but there is a filter: %s", whole)
	}
	p.Mirror, p.Flip = true, true
	if flipped := strings.Join(clipArgs(p, "out.part", false), " "); !strings.Contains(flipped, "-vf hflip,vflip") {
		t.Errorf("a mirrored, upside-down source isn't put right: %s", flipped)
	}
}

// writeRecording puts a race folder with a Race.json and a recording in dir.
func writeRecording(t *testing.T, dir, raceID string, withVideo bool, flip string) {
	t.Helper()
	raceDir := filepath.Join(dir, "ev", raceID)
	if err := os.MkdirAll(raceDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Alice (p1) on Raceband 1: holeshot, then laps 1-3 of 10s from 14:00:10.
	var dets, laps []string
	for n := 0; n <= 3; n++ {
		end := fmt.Sprintf("2026/11/08 14:00:%02d", 10+n*10)
		start := fmt.Sprintf("2026/11/08 14:00:%02d", n*10)
		dets = append(dets, fmt.Sprintf(`{"ID":"d%d","Pilot":"p1","Valid":true,"IsLapEnd":true,"Time":"%s"}`, n, end))
		laps = append(laps, fmt.Sprintf(`{"ID":"l%d","Detection":"d%d","LapNumber":%d,"LengthSeconds":10,"StartTime":"%s","EndTime":"%s"}`, n, n, n, start, end))
	}
	race := fmt.Sprintf(`[{"ID":"%s","Round":"r","Valid":true,
		"PilotChannels":[{"Pilot":"p1","Channel":"00000009-0000-0000-0000-000000000000"},{"Pilot":"p2","Channel":"0000000a-0000-0000-0000-000000000000"}],
		"Detections":[%s],"Laps":[%s]}]`, raceID, strings.Join(dets, ","), strings.Join(laps, ","))
	os.WriteFile(filepath.Join(raceDir, "Race.json"), []byte(race), 0644)

	// The recording began at 13:59:55, in summer time, 5s before the race.
	info := `<?xml version="1.0" encoding="utf-8"?>
<ArrayOfRecodingInfo>
  <RecodingInfo>
    <FilePath>events\ev\` + raceID + `\0.mp4</FilePath>
    <ChannelCoveragePercent>100</ChannelCoveragePercent>
    <FlipMirrored>` + flip + `</FlipMirrored>
    <ChannelBounds>
      <VideoBounds><Channel>Raceband 1</Channel><RelativeSourceBounds><X>0</X><Y>0</Y><Width>0.5</Width><Height>0.5</Height></RelativeSourceBounds><SourceType>FPVFeed</SourceType></VideoBounds>
      <VideoBounds><Channel>Raceband 2</Channel><RelativeSourceBounds><X>0.5</X><Y>0</Y><Width>0.5</Width><Height>0.5</Height></RelativeSourceBounds><SourceType>FPVFeed</SourceType></VideoBounds>
      <VideoBounds><Channel></Channel><RelativeSourceBounds><X>0</X><Y>0.5</Y><Width>1</Width><Height>0.5</Height></RelativeSourceBounds><SourceType>Launch</SourceType></VideoBounds>
    </ChannelBounds>
    <FrameTimes>
      <FrameTime><Frame>1</Frame><Time>2026-11-08T13:59:55+01:00</Time><Seconds>0</Seconds></FrameTime>
      <FrameTime><Frame>1800</Frame><Time>2026-11-08T14:00:25+01:00</Time><Seconds>30</Seconds></FrameTime>
      <FrameTime><Frame>3300</Frame><Time>2026-11-08T14:00:50+01:00</Time><Seconds>55</Seconds></FrameTime>
    </FrameTimes>
    <DeviceLatency>0</DeviceLatency>
    <FrameWork>MediaFoundation</FrameWork>
  </RecodingInfo>
</ArrayOfRecodingInfo>`
	os.WriteFile(filepath.Join(raceDir, "0.recordinfo.xml"), []byte("\xef\xbb\xbf"+info), 0644)
	if withVideo {
		os.WriteFile(filepath.Join(raceDir, "0.mp4"), []byte("not really a video"), 0644)
	}
}

func TestPlanClips(t *testing.T) {
	dir := t.TempDir()
	writeRecording(t, dir, "recorded", true, "None")
	writeRecording(t, dir, "deleted", false, "None")
	writeRecording(t, dir, "mirrored", true, "FlippedAndMirrored")
	os.MkdirAll(filepath.Join(dir, "ev", "unrecorded"), 0755)
	src, _ := os.ReadFile(filepath.Join(dir, "ev", "recorded", "Race.json"))
	os.WriteFile(filepath.Join(dir, "ev", "unrecorded", "Race.json"), []byte(strings.ReplaceAll(string(src), `"ID":"recorded"`, `"ID":"unrecorded"`)), 0644)
	os.WriteFile(filepath.Join(dir, "ev", "Pilots.json"), []byte(`[{"ID":"p1","Name":"Alice"}]`), 0644)

	e := newEventData(dir, "ev", "Test")
	if _, err := e.refresh(); err != nil {
		t.Fatal(err)
	}
	run := func(race string) lapRun {
		return lapRun{Name: "Alice", Total: 30, RaceID: race, PilotID: "p1", LastLap: 3}
	}
	plans := planClips(e, []lapRun{run("recorded"), run("deleted"), run("unrecorded"), run("mirrored"), run("gone")})

	// Laps 1-3 run 14:00:10 to 14:00:40; the video began at 13:59:55, so the
	// clip, 3s either side, is from 12s in and 36s long.
	p := plans[0]
	if p.Problem != "" || !near(p.Start, 12) || !near(p.Length, 36) || filepath.Base(p.Video) != "0.mp4" {
		t.Errorf("recorded: %+v", p)
	}
	if !p.HasCrop || p.Crop != (relRect{0, 0, 0.5, 0.5}) || p.Mirror || p.Flip {
		t.Errorf("Alice's picture is the top left quarter: %+v", p)
	}
	if plans[1].Problem != "The recording of this race has been deleted" {
		t.Errorf("deleted: %q", plans[1].Problem)
	}
	if plans[2].Problem != "This race wasn't recorded" {
		t.Errorf("unrecorded: %q", plans[2].Problem)
	}
	// A source shown mirrored and upside down: her picture is at the opposite
	// corner of the file, and the clip is turned back.
	if m := plans[3]; m.Problem != "" || m.Crop != (relRect{0.5, 0.5, 0.5, 0.5}) || !m.Mirror || !m.Flip {
		t.Errorf("mirrored: %+v", m)
	}
	if plans[4].Problem == "" {
		t.Error("a race that isn't there has a clip")
	}

	// A pilot on a channel the recording has no picture for still gets the clip, uncropped.
	e.races["recorded"].Channels["p1"] = "00000004-0000-0000-0000-000000000000"
	if q := planClips(e, []lapRun{run("recorded")})[0]; q.Problem != "" || q.HasCrop {
		t.Errorf("no picture marked for the channel: %+v", q)
	}
}

func TestTracksideRecords(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "data", "Profile 1")
	os.MkdirAll(profile, 0755)
	if _, known := tracksideRecords(dir, "Profile 1"); known {
		t.Error("no settings file, yet it claims to know")
	}
	write := func(a, b string) {
		os.WriteFile(filepath.Join(profile, "VideoSettings.xml"), []byte(`<?xml version="1.0"?><ArrayOfVideoConfig>
			<VideoConfig><DeviceName>A</DeviceName><RecordVideoForReplays>`+a+`</RecordVideoForReplays></VideoConfig>
			<VideoConfig><DeviceName>B</DeviceName><RecordVideoForReplays>`+b+`</RecordVideoForReplays></VideoConfig>
			</ArrayOfVideoConfig>`), 0644)
	}
	write("false", "true")
	if on, known := tracksideRecords(dir, "Profile 1"); !on || !known {
		t.Errorf("one source records: on=%v known=%v", on, known)
	}
	write("false", "false")
	if on, known := tracksideRecords(dir, "Profile 1"); on || !known {
		t.Errorf("no source records: on=%v known=%v", on, known)
	}
}
