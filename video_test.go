package main

import (
	"bytes"
	"fmt"
	"image/png"
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
	args := strings.Join(clipArgs(p, "out.part", clipOptions{Crop: true}, 0), " ")
	for _, want := range []string{
		`-ss 1795.391 -t 46.580 -i C:\ev\race\0.mp4`, // seeks before reading, so it is quick an hour in
		"-vf crop=2*round(iw*0.937500/2)-2*round(iw*0.562500/2):2*round(ih*0.500000/2)-2*round(ih*0.000000/2):2*round(iw*0.562500/2):2*round(ih*0.000000/2)",
		"-c:v libx264", "-f mp4 out.part",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("arguments lack %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "drawtext") {
		t.Errorf("no timer asked for, but one is drawn: %s", args)
	}
	if whole := strings.Join(clipArgs(p, "out.part", clipOptions{}, 0), " "); strings.Contains(whole, "-vf") || strings.Contains(whole, "filter_complex") {
		t.Errorf("whole picture asked for, but there is a filter: %s", whole)
	}
	p.Mirror, p.Flip = true, true
	if flipped := strings.Join(clipArgs(p, "out.part", clipOptions{}, 0), " "); !strings.Contains(flipped, "-vf hflip,vflip") {
		t.Errorf("a mirrored, upside-down source isn't put right: %s", flipped)
	}

	// argAfter is the argument that follows a flag.
	argAfter := func(args []string, flag string) string {
		for i, a := range args {
			if a == flag && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	// order checks that parts come one after another in s.
	order := func(what, s string, parts ...string) {
		t.Helper()
		at := 0
		for _, part := range parts {
			i := strings.Index(s[at:], part)
			if i < 0 {
				t.Errorf("%s: %q is missing, or out of order, in\n%s", what, part, s)
				return
			}
			at += i + len(part)
		}
	}

	// The laps: 13.613, 13.153 and 13.814, the first starting 3s into the clip.
	p.LapsAt = 3
	p.Run = lapRun{Total: 40.58, Lap1: 13.613, Lap2: 13.153, Lap3: 13.814}

	// The timer and lap times are drawn last: on the pilot's own picture, and
	// not mirrored with it. Each lap's time shows from the moment it ends.
	timed := clipArgs(p, "out.part", clipOptions{Crop: true, Timer: true}, 0)
	order("timer", argAfter(timed, "-vf"), "crop=", ",hflip,vflip,",
		"drawtext=fontfile=font.ttf:textfile=timer.txt:fontsize=h/11:",
		"drawtext=fontfile=font.ttf:textfile=lap1.txt:fontsize=h/17:", "+0*(h/17+10):enable='gte(t,16.613)'",
		"textfile=lap2.txt:", "+1*(h/17+10):enable='gte(t,29.766)'",
		"textfile=lap3.txt:", "+2*(h/17+10):enable='gte(t,43.580)'")

	// The logo is a second input, laid over the picture before the timer.
	all := clipArgs(p, "out.part", clipOptions{Crop: true, Timer: true, Logo: true}, 120)
	order("logo arguments", strings.Join(all, " "), `-i C:\ev\race\0.mp4`, "-i logo.png", "-filter_complex", "-map [out] -map 0:a?")
	order("logo filters", argAfter(all, "-filter_complex"), "[0:v]crop=", ",hflip,vflip[pic];",
		"[1:v]scale=120:-2,format=rgba,colorchannelmixer=aa=0.85[logo];",
		"[pic][logo]overlay=x=W-w-W/40:y=H-h-W/40,", "textfile=timer.txt", "textfile=lap3.txt", "[out]")
	if argAfter(all, "-vf") != "" {
		t.Error("both -vf and -filter_complex given")
	}

	// The logo alone, on the whole picture of a source that isn't turned.
	p.Mirror, p.Flip = false, false
	if g := argAfter(clipArgs(p, "out.part", clipOptions{Logo: true}, 320), "-filter_complex"); g != "[0:v]null[pic];[1:v]scale=320:-2,format=rgba,colorchannelmixer=aa=0.85[logo];[pic][logo]overlay=x=W-w-W/40:y=H-h-W/40[out]" {
		t.Errorf("logo alone: %s", g)
	}
	// Asked for, but the video's size couldn't be read: the clip is made without it.
	if none := strings.Join(clipArgs(p, "out.part", clipOptions{Logo: true}, 0), " "); strings.Contains(none, "logo") {
		t.Errorf("no size for the logo, yet it is drawn: %s", none)
	}
}

func TestClockAndLapLines(t *testing.T) {
	for secs, want := range map[float64]string{
		0: "0.000", 4.25: "4.250", 13.6129999: "13.613", 59.9996: "1:00.000", 62.345: "1:02.345", 754.001: "12:34.001",
	} {
		if got := clock(secs); got != want {
			t.Errorf("clock(%v) = %q, want %q", secs, got, want)
		}
	}
	text, from := lapLines(clipPlan{LapsAt: 3, Run: lapRun{Lap1: 13.613, Lap2: 13.153, Lap3: 61.5}})
	if text != [3]string{"L1 13.613", "L2 13.153", "L3 1:01.500"} {
		t.Errorf("lap lines: %q", text)
	}
	if !near(from[0], 16.613) || !near(from[1], 29.766) || !near(from[2], 91.266) {
		t.Errorf("shown from: %v", from)
	}
}

func TestLogoSize(t *testing.T) {
	p := clipPlan{HasCrop: true, Crop: relRect{0.564375, 0.0025, 0.37125, 0.495}} // 99% of one pilot's rectangle
	// Cropped out of a 1280-wide video the picture is 476 wide; the logo a quarter of that.
	if got := logoWidth(p, clipOptions{Crop: true}, 1280); got != 120 {
		t.Errorf("on the pilot's own picture: %d, want 120", got)
	}
	if got := logoWidth(p, clipOptions{}, 1280); got != 320 {
		t.Errorf("on the whole picture: %d, want 320", got)
	}
	if got := logoWidth(clipPlan{}, clipOptions{}, 64); got != 32 {
		t.Errorf("never tiny: %d", got)
	}

	report := `Input #0, mov,mp4,m4a,3gp,3g2,mj2, from 'C:\ev\0.mp4':
  Duration: 00:35:29.98, start: 0.000000, bitrate: 23423 kb/s
  Stream #0:0[0x1](und): Video: h264 (Main) (avc1 / 0x31637661), yuv420p(progressive), 1280x720 [SAR 1:1 DAR 16:9], 23419 kb/s, 60 fps, 60 tbr, 60k tbn (default)
At least one output file must be specified`
	if w, h, ok := parseVideoSize(report); !ok || w != 1280 || h != 720 {
		t.Errorf("size read as %dx%d (%v)", w, h, ok)
	}
	if _, _, ok := parseVideoSize("C:\\ev\\0.mp4: No such file or directory"); ok {
		t.Error("a size was read from an error message")
	}
}

func TestBuiltInLogo(t *testing.T) {
	img, err := png.Decode(bytes.NewReader(builtInLogo))
	if err != nil {
		t.Fatalf("the built-in logo isn't a PNG: %v", err)
	}
	b := img.Bounds()
	// Its corners are background, and have to be see-through.
	for _, at := range [][2]int{{b.Min.X, b.Min.Y}, {b.Max.X - 1, b.Min.Y}, {b.Min.X, b.Max.Y - 1}, {b.Max.X - 1, b.Max.Y - 1}} {
		if _, _, _, a := img.At(at[0], at[1]).RGBA(); a != 0 {
			t.Errorf("the logo's corner at %v isn't see-through (alpha %d)", at, a>>8)
		}
	}
	// And its middle is the logo.
	if _, _, _, a := img.At((b.Min.X+b.Max.X)/2, (b.Min.Y+b.Max.Y)/2).RGBA(); a>>8 != 255 {
		t.Errorf("the logo's middle is see-through (alpha %d)", a>>8)
	}
}

func TestTimerText(t *testing.T) {
	// Under a minute: seconds and milliseconds. The first lap starts 3s in
	// and the three laps take 24.617s.
	want := "%{eif:floor(round(clip(t-3.000,0,24.617)*1000)/1000):d}.%{eif:mod(round(clip(t-3.000,0,24.617)*1000),1000):d:3}"
	if got := timerText(3, 24.617); got != want {
		t.Errorf("short run:\n got %s\nwant %s", got, want)
	}
	// A minute or more: minutes too, and seconds always two digits.
	long := timerText(1.25, 84.2)
	for _, part := range []string{
		"%{eif:floor(round(clip(t-1.250,0,84.200)*1000)/60000):d}:",
		":%{eif:floor(mod(round(clip(t-1.250,0,84.200)*1000),60000)/1000):d:2}.",
		".%{eif:mod(round(clip(t-1.250,0,84.200)*1000),1000):d:3}",
	} {
		if !strings.Contains(long, part) {
			t.Errorf("long run lacks %q:\n%s", part, long)
		}
	}
	if strings.ContainsAny(timerText(3, 24.617)+long, "\r\n") {
		t.Error("a line break in the timer's text would be drawn")
	}
}

func TestOverlayFolder(t *testing.T) {
	dir, err := overlayFolder(clipOptions{Timer: true, Logo: true}, builtInLogo)
	if err != nil {
		t.Skipf("no font on this machine: %v", err)
	}
	defer os.RemoveAll(dir)
	if st, err := os.Stat(filepath.Join(dir, overlayFontFile)); err != nil || st.Size() < 1000 {
		t.Errorf("the font isn't in the folder: %v", err)
	}
	if st, err := os.Stat(filepath.Join(dir, logoFile)); err != nil || st.Size() != int64(len(builtInLogo)) {
		t.Errorf("the logo isn't in the folder: %v", err)
	}

	// Only what is asked for is put there, and a missing logo is an error.
	plain, err := overlayFolder(clipOptions{Logo: true}, builtInLogo)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(plain)
	if _, err := os.Stat(filepath.Join(plain, overlayFontFile)); err == nil {
		t.Error("a font was put there with no timer asked for")
	}
	if _, err := overlayFolder(clipOptions{Logo: true}, nil); err == nil {
		t.Error("no logo, and no error")
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
	if p.Problem != "" || !near(p.Start, 12) || !near(p.Length, 36) || !near(p.LapsAt, 3) || filepath.Base(p.Video) != "0.mp4" {
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
