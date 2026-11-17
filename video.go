package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows/registry"
)

// Video clips of pilots' 3-lap runs, cut out of the recordings FPVTrackside
// makes of its races.
//
// Trackside saves a race's video in the race's folder (0.mp4, one per video
// source that is set to record) with a <name>.recordinfo.xml beside it. That
// file says where each channel's picture is in the frame, and at what moment
// frames of the video were captured, which is how a lap's time is found in it.

// clipPad is how much video is kept before the first lap and after the last.
const clipPad = 3 * time.Second

// relRect is a rectangle in a video frame, as fractions of its width and height.
type relRect struct{ X, Y, W, H float64 }

// frameAnchor is a moment Trackside noted while recording.
type frameAnchor struct {
	At      time.Time // read the way Race.json's times are; see wallClock
	Seconds float64   // how far into the video that moment is
}

// recording is one video of a race.
type recording struct {
	Video   string             // the video file
	Bounds  map[string]relRect // where each channel's picture is, by the channel's name in lower case
	Anchors []frameAnchor      // in time order, at least one
	Latency time.Duration      // the video source's delay, as set in Trackside
	// Trackside can show a video source mirrored or upside down. Unless it
	// recorded through ffmpeg, the file has the picture as the camera sent
	// it, and these say what has to be done to it to look as it did on screen.
	Mirror, Flip bool
}

// wallClock re-reads a time with a zone as Race.json's times are read.
// Race.json has local times without a zone, which parseTracksideTime reads as
// if they were UTC; a recording's times carry their zone. Keeping the clock
// reading and dropping the zone puts both on the same footing, whatever zone
// this computer is in.
func wallClock(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

// parseRecordTime reads a time from a recordinfo file.
func parseRecordTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		if strings.HasSuffix(s, "Z") {
			t = t.Local() // written as UTC: the clock reading is the local one
		}
		return wallClock(t), nil
	}
	return time.Parse("2006-01-02T15:04:05.999999999", s) // no zone: already a clock reading
}

type recordInfoXML struct {
	Infos []struct {
		FilePath     string  `xml:"FilePath"`
		Coverage     float64 `xml:"ChannelCoveragePercent"`
		FlipMirrored string  `xml:"FlipMirrored"` // None, Flipped, Mirrored, FlippedAndMirrored
		FrameWork    string  `xml:"FrameWork"`
		Bounds       []struct {
			Channel string `xml:"Channel"`
			Rect    struct {
				X      float64 `xml:"X"`
				Y      float64 `xml:"Y"`
				Width  float64 `xml:"Width"`
				Height float64 `xml:"Height"`
			} `xml:"RelativeSourceBounds"`
			SourceType string `xml:"SourceType"`
		} `xml:"ChannelBounds>VideoBounds"`
		Frames []struct {
			Time    string  `xml:"Time"`
			Seconds float64 `xml:"Seconds"`
		} `xml:"FrameTimes>FrameTime"`
		DeviceLatency float64 `xml:"DeviceLatency"`
	} `xml:"RecodingInfo"` // Trackside's spelling
}

// readRecordings reads the recordings in a race's folder. missing counts those
// Trackside made whose video file is no longer there: videos are big, and get
// deleted or moved while the small recordinfo files stay.
func readRecordings(raceDir string) (recs []recording, missing int) {
	files, _ := filepath.Glob(filepath.Join(raceDir, "*.recordinfo.xml"))
	sort.Strings(files)
	for _, file := range files {
		var doc recordInfoXML
		if err := readTracksideXML(file, &doc); err != nil {
			continue
		}
		for _, info := range doc.Infos {
			// FilePath is relative to Trackside's folder; the video is beside the XML.
			name := filepath.Base(strings.ReplaceAll(info.FilePath, `\`, "/"))
			if info.FilePath == "" {
				name = strings.TrimSuffix(filepath.Base(file), ".recordinfo.xml") + ".mp4"
			}
			video := filepath.Join(raceDir, name)
			if st, err := os.Stat(video); err != nil || st.IsDir() || st.Size() == 0 {
				missing++
				continue
			}
			rec := recording{
				Video:   video,
				Bounds:  map[string]relRect{},
				Latency: time.Duration(info.DeviceLatency * float64(time.Second)),
			}
			if info.FrameWork != "FFmpeg" {
				flip := strings.ToLower(info.FlipMirrored)
				rec.Mirror = strings.Contains(flip, "mirror")
				rec.Flip = strings.Contains(flip, "flip")
			}
			// Trackside shows a little less than a channel's whole rectangle,
			// to keep its neighbours' edges out: this share of it, about its centre.
			share := info.Coverage / 100
			if share <= 0 || share > 1 {
				share = 1
			}
			for _, b := range info.Bounds {
				if b.SourceType != "" && b.SourceType != "FPVFeed" {
					continue // a commentator or launch camera, not a pilot's picture
				}
				if b.Rect.Width <= 0 || b.Rect.Height <= 0 {
					continue
				}
				r := relRect{W: b.Rect.Width * share, H: b.Rect.Height * share}
				r.X = b.Rect.X + (b.Rect.Width-r.W)/2
				r.Y = b.Rect.Y + (b.Rect.Height-r.H)/2
				// Bounds are of the picture as shown; turn them into the file's.
				if rec.Mirror {
					r.X = 1 - r.X - r.W
				}
				if rec.Flip {
					r.Y = 1 - r.Y - r.H
				}
				rec.Bounds[strings.ToLower(strings.TrimSpace(b.Channel))] = r
			}
			for _, f := range info.Frames {
				at, err := parseRecordTime(f.Time)
				if err != nil {
					continue
				}
				rec.Anchors = append(rec.Anchors, frameAnchor{at, f.Seconds})
			}
			if len(rec.Anchors) == 0 {
				continue // nothing to find a lap in it by
			}
			sort.Slice(rec.Anchors, func(i, j int) bool { return rec.Anchors[i].At.Before(rec.Anchors[j].At) })
			recs = append(recs, rec)
		}
	}
	return recs, missing
}

// position is how far into the video, in seconds from its first frame, the
// moment t is. It is worked out as Trackside does for its own replays: from
// the nearest moment it noted, plus the video source's delay.
func (r recording) position(t time.Time) float64 {
	best := r.Anchors[0]
	for _, a := range r.Anchors[1:] {
		if t.Sub(a.At).Abs() < t.Sub(best.At).Abs() {
			best = a
		}
	}
	// The noted positions count from wherever the recorder's clock started,
	// which isn't always 0; ffmpeg seeks from the first frame.
	return best.Seconds - r.Anchors[0].Seconds + t.Sub(best.At).Seconds() + r.Latency.Seconds()
}

// covers reports whether the recording was running from one moment to another.
// Trackside notes a moment every few seconds, so the video runs on a little
// past the last one.
func (r recording) covers(from, to time.Time) bool {
	first, last := r.Anchors[0].At, r.Anchors[len(r.Anchors)-1].At
	return !from.Before(first.Add(-time.Second)) && !to.After(last.Add(10*time.Second))
}

// channelBands is Trackside's table of channels: the IDs it gives them, and
// the band, prefix and number its names for them are made from.
var channelBands = []struct {
	first, last int
	band        string
	prefix      string
}{
	{1, 8, "Fatshark", ""},
	{9, 16, "Raceband", ""},
	{17, 24, "A", ""},
	{25, 32, "B", ""},
	{33, 40, "DJIFPVHD", ""},
	{41, 48, "E", ""},
	{49, 56, "HDZero", "R"},
	{59, 66, "LowBand", ""},
	{67, 74, "Diatone", ""},
	{75, 81, "DJIO3", "O"},
	{82, 89, "HDZero", "L"},
	{100, 107, "DJIO4", "R"},
	{110, 117, "WalkSnail", "R"},
}

// channelNames returns the names a recording may have for a channel, given
// the channel's ID as Race.json has it: a GUID whose first part is Trackside's
// number for the channel. They are in lower case, as recording.Bounds is.
func channelNames(id string) []string {
	head, _, _ := strings.Cut(id, "-")
	n64, err := strconv.ParseInt(head, 16, 32)
	if err != nil {
		return nil
	}
	n := int(n64)
	band, label := "", ""
	switch n {
	case 57:
		band, label = "HDZero", "F2"
	case 58:
		band, label = "HDZero", "F4"
	default:
		for _, b := range channelBands {
			if n >= b.first && n <= b.last {
				band, label = b.band, b.prefix+strconv.Itoa(n-b.first+1)
			}
		}
	}
	if band == "" {
		return nil
	}
	names := []string{strings.ToLower(band + " " + label)}
	if band == "HDZero" {
		// Trackside has two names for this band and may write either.
		names = append(names, strings.ToLower("SharkByte "+label))
	}
	return names
}

// clipPlan is the clip to cut for one pilot's run, or why there isn't one.
type clipPlan struct {
	Run     lapRun
	Video   string
	Start   float64 // seconds into the video where the clip starts
	Length  float64 // seconds
	LapsAt  float64 // seconds into the clip where the first lap starts
	Crop    relRect // where the pilot's own picture is in the video's frame
	HasCrop bool
	Mirror  bool   // the picture has to be mirrored, or turned upside down,
	Flip    bool   // to look as it did in Trackside
	Problem string // why no clip can be cut; "" if one can
	// MCS is the time on MCS the clip would be the DVR of, when clips can be
	// put on the league's YouTube channel; see matchVideos.
	MCS videoMatch
}

// planClips works out, for each run, which recording has it and where.
func planClips(e *eventData, runs []lapRun) []clipPlan {
	type found struct {
		recs    []recording
		missing int
	}
	byRace := map[string]found{}
	plans := make([]clipPlan, len(runs))
	for i, run := range runs {
		p := &plans[i]
		p.Run = run
		race := e.races[run.RaceID]
		if race == nil {
			p.Problem = "The race is no longer in the event"
			continue
		}

		// The run is the three laps up to LastLap.
		var from, to time.Time
		for _, lap := range race.Laps {
			if lap.PilotID != run.PilotID {
				continue
			}
			if lap.Number == run.LastLap-2 && from.IsZero() {
				from = lap.Start
			}
			if lap.Number == run.LastLap && to.IsZero() {
				to = lap.End
			}
		}
		if from.IsZero() || to.IsZero() || !to.After(from) {
			p.Problem = "Trackside has no times for these laps"
			continue
		}
		firstLap := from
		from, to = from.Add(-clipPad), to.Add(clipPad)

		f, ok := byRace[run.RaceID]
		if !ok {
			f.recs, f.missing = readRecordings(filepath.Join(e.dir, run.RaceID))
			byRace[run.RaceID] = f
		}
		if len(f.recs) == 0 {
			p.Problem = "This race wasn't recorded"
			if f.missing > 0 {
				p.Problem = "The recording of this race has been deleted"
			}
			continue
		}

		// The recording to use: one running during the run, and if there is
		// more than one (several video sources), the one with the pilot's
		// picture in it.
		names := channelNames(race.Channels[run.PilotID])
		var rec *recording
		for j := range f.recs {
			r := &f.recs[j]
			if !r.covers(from, to) {
				continue
			}
			crop, hasCrop := relRect{}, false
			for _, name := range names {
				if b, ok := r.Bounds[name]; ok {
					crop, hasCrop = b, true
					break
				}
			}
			if rec == nil || (hasCrop && !p.HasCrop) {
				rec, p.Crop, p.HasCrop = r, crop, hasCrop
			}
		}
		if rec == nil {
			p.Problem = "The recording doesn't cover these laps"
			continue
		}
		p.Video, p.Mirror, p.Flip = rec.Video, rec.Mirror, rec.Flip
		start, end := rec.position(from), rec.position(to)
		if start < 0 {
			start = 0
		}
		if end-start < 1 {
			p.Problem = "The recording doesn't cover these laps"
			continue
		}
		p.Start, p.Length = start, end-start
		p.LapsAt = rec.position(firstLap) - start
	}
	return plans
}

// tracksideRecords reports whether the FPVTrackside profile has a video
// source set to record races ("Record Video For Replays"). known is false if
// its video settings can't be read.
func tracksideRecords(tracksideDir, profile string) (records, known bool) {
	var doc struct {
		Sources []struct {
			Record string `xml:"RecordVideoForReplays"`
		} `xml:"VideoConfig"`
	}
	if err := readTracksideXML(filepath.Join(tracksideDir, "data", profile, "VideoSettings.xml"), &doc); err != nil {
		return false, false
	}
	for _, s := range doc.Sources {
		if strings.EqualFold(strings.TrimSpace(s.Record), "true") {
			return true, true
		}
	}
	return false, true
}

// clipFileName is the name a pilot's clip is saved under.
func clipFileName(track, pilot string, total float64) string {
	return cleanFileName(fmt.Sprintf("MCS - %s - %s - %.3f", track, pilot, total)) + ".mp4"
}

// cleanFileName takes out of a name what Windows won't have in a file name.
func cleanFileName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Join(strings.Fields(name), " ")
	name = strings.TrimRight(name, ". ")
	if r := []rune(name); len(r) > 180 {
		name = strings.TrimRight(string(r[:180]), ". ")
	}
	return name
}

// cropFilter is the ffmpeg filter that keeps one rectangle of the picture.
// Its edges land on even pixels, as the encoder needs.
func cropFilter(r relRect) string {
	edge := func(size string, at float64) string {
		return fmt.Sprintf("2*round(%s*%s/2)", size, strconv.FormatFloat(at, 'f', 6, 64))
	}
	left, right := edge("iw", r.X), edge("iw", r.X+r.W)
	top, bottom := edge("ih", r.Y), edge("ih", r.Y+r.H)
	return fmt.Sprintf("crop=%s-%s:%s-%s:%s:%s", right, left, bottom, top, left, top)
}

// clipOptions are the choices made about how clips are cut.
type clipOptions struct {
	Crop  bool // just the pilot's own picture, where the recording says where that is
	Timer bool // a timer on the picture, running over the three laps, and each lap's time under it
	Logo  bool // the MCS logo in a corner of the picture
	// A picture less than this many pixels high is enlarged to it; 0 leaves
	// every picture the size it is. One pilot's picture out of four is small,
	// and video sites give small videos so little data that they look bad.
	MinHeight int
	// Upload puts each clip, once cut, on the league's YouTube channel
	// through the MCS site, against the pilot's time there. It doesn't change
	// how a clip is cut.
	Upload bool
}

// defaultMinHeight is the height small clips are enlarged to unless the
// settings say otherwise: enough for video sites to treat a clip as HD.
const defaultMinHeight = 720

// What is put on the picture is drawn by ffmpeg from files it finds in the
// folder it is run in (see overlayFolder), so that no path has to be written
// into a filter, where colons, quotes and backslashes all mean something.
const (
	overlayFontFile = "font.ttf"
	timerTextFile   = "timer.txt"
	lapTextFile     = "lap%d.txt" // 1 to 3
	logoFile        = "logo.png"

	// White on a dark box, so it reads over any picture.
	textStyle = ":fontcolor=white:box=1:boxcolor=black@0.55"
	// The timer: top right, where pilots' on-screen displays tend to have
	// nothing, and sized to the picture.
	timerFilter = "drawtext=fontfile=" + overlayFontFile + ":textfile=" + timerTextFile +
		":fontsize=h/11" + textStyle + ":boxborderw=6:x=w-text_w-w/30:y=h/30"
	// A lap's time: under the timer, a little smaller, one line a lap, from
	// the moment the lap is finished.
	lapFilter = "drawtext=fontfile=" + overlayFontFile + ":textfile=" + lapTextFile +
		":fontsize=h/17" + textStyle + ":boxborderw=4:x=w-text_w-w/30:y=h/30+h/11+16+%d*(h/17+10):enable='gte(t,%.3f)'"

	// The logo: bottom right, a quarter of the picture's width, and slightly
	// see-through.
	logoShare   = 0.25
	logoOverlay = "format=rgba,colorchannelmixer=aa=0.85[logo];[pic][logo]overlay=x=W-w-W/40:y=H-h-W/40"
)

// clock writes a time as the timer shows it: 24.617, or 1:24.617 for a
// minute or more.
func clock(seconds float64) string {
	ms := int64(seconds*1000 + 0.5)
	if ms < 60000 {
		return fmt.Sprintf("%d.%03d", ms/1000, ms%1000)
	}
	return fmt.Sprintf("%d:%02d.%03d", ms/60000, ms%60000/1000, ms%1000)
}

// lapLines are the three laps of a plan's run as they are shown under the
// timer, and when in the clip each is shown from: the moment the lap ends.
func lapLines(p clipPlan) (text [3]string, from [3]float64) {
	at := p.LapsAt
	for i, lap := range []float64{p.Run.Lap1, p.Run.Lap2, p.Run.Lap3} {
		at += lap
		text[i] = fmt.Sprintf("L%d %s", i+1, clock(lap))
		from[i] = at
	}
	return text, from
}

// timerText is what ffmpeg is given to draw as the timer: text that it works
// out afresh for every frame from the frame's time in the clip, t. The timer
// shows 0 until the first lap starts, lapsAt seconds in, runs for the three
// laps, and stops on their total. It reads to the millisecond: 24.617, or
// 1:24.617 when the total is a minute or more.
func timerText(lapsAt, total float64) string {
	// The time shown, in whole milliseconds, so the parts below agree.
	ms := fmt.Sprintf("round(clip(t-%.3f,0,%.3f)*1000)", lapsAt, total)
	if total < 60 {
		return fmt.Sprintf("%%{eif:floor(%s/1000):d}.%%{eif:mod(%s,1000):d:3}", ms, ms)
	}
	return fmt.Sprintf("%%{eif:floor(%s/60000):d}:%%{eif:floor(mod(%s,60000)/1000):d:2}.%%{eif:mod(%s,1000):d:3}", ms, ms, ms)
}

// overlayFolder makes a folder for ffmpeg to run in when it draws on clips,
// with what the options need in it: a font for the timer, one whose digits
// are all one width so the timer doesn't shake as it counts, and the logo.
func overlayFolder(opts clipOptions, logo []byte) (string, error) {
	dir, err := os.MkdirTemp("", "mcs-uploader-")
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		os.RemoveAll(dir)
		return "", err
	}
	if opts.Timer {
		fonts := filepath.Join(os.Getenv("WINDIR"), "Fonts")
		var font []byte
		for _, name := range []string{"consola.ttf", "lucon.ttf", "cour.ttf", "arial.ttf"} {
			if data, err := os.ReadFile(filepath.Join(fonts, name)); err == nil {
				font = data
				break
			}
		}
		if font == nil {
			return fail(fmt.Errorf("no font for the timer was found in %s", fonts))
		}
		if err := os.WriteFile(filepath.Join(dir, overlayFontFile), font, 0600); err != nil {
			return fail(err)
		}
	}
	if opts.Logo {
		if len(logo) == 0 {
			return fail(errors.New("there is no logo to put on the videos"))
		}
		if err := os.WriteFile(filepath.Join(dir, logoFile), logo, 0600); err != nil {
			return fail(err)
		}
	}
	return dir, nil
}

// evenPixels rounds a length in pixels to the nearest even number, as the
// crop filter does.
func evenPixels(x float64) int { return 2 * int(x/2+0.5) }

// pictureSize is the size of a plan's clip, given the size of the video it
// is cut from: cropped to the pilot's picture, then enlarged if it is small.
func pictureSize(p clipPlan, opts clipOptions, videoW, videoH int) (width, height int) {
	width, height = videoW, videoH
	if opts.Crop && p.HasCrop {
		w, h := float64(videoW), float64(videoH)
		width = evenPixels((p.Crop.X+p.Crop.W)*w) - evenPixels(p.Crop.X*w)
		height = evenPixels((p.Crop.Y+p.Crop.H)*h) - evenPixels(p.Crop.Y*h)
	}
	if height > 0 && height < opts.MinHeight {
		width = evenPixels(float64(width) * float64(opts.MinHeight) / float64(height))
		height = opts.MinHeight
	}
	return width, height
}

// logoWidth is how wide the logo is drawn on a plan's clip, given the size
// of the video the clip is cut from.
func logoWidth(p clipPlan, opts clipOptions, videoW, videoH int) int {
	width, _ := pictureSize(p, opts, videoW, videoH)
	return max(32, evenPixels(float64(width)*logoShare))
}

var videoSizeRE = regexp.MustCompile(`Video:.*?\b(\d{2,5})x(\d{2,5})\b`)

// parseVideoSize finds a video's width and height in what ffmpeg says about it.
func parseVideoSize(report string) (width, height int, ok bool) {
	m := videoSizeRE.FindStringSubmatch(report)
	if m == nil {
		return 0, 0, false
	}
	width, _ = strconv.Atoi(m[1])
	height, _ = strconv.Atoi(m[2])
	return width, height, width > 0 && height > 0
}

// videoSize asks ffmpeg for a video's width and height.
func videoSize(ctx context.Context, ffmpeg, video string) (width, height int, ok bool) {
	// With no output named ffmpeg describes the input and stops.
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-nostdin", "-i", video)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Run() // "fails", for want of an output
	return parseVideoSize(stderr.String())
}

// clipArgs are ffmpeg's arguments for cutting a plan's clip into the file
// out. logoW is the width to draw the logo at, 0 for no logo.
//
// The clip is encoded afresh rather than copied: a copy can only start on a
// key frame, seconds away from where it should, and can't be cropped or
// drawn on.
func clipArgs(p clipPlan, out string, opts clipOptions, logoW int) []string {
	secs := func(s float64) string { return strconv.FormatFloat(s, 'f', 3, 64) }
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-ss", secs(p.Start), "-t", secs(p.Length), "-i", p.Video,
	}

	// The picture itself, then what is drawn on it: drawn last, so it isn't
	// cropped or turned over with the picture.
	var picture, drawn []string
	if opts.Crop && p.HasCrop {
		picture = append(picture, cropFilter(p.Crop))
	}
	if p.Mirror {
		picture = append(picture, "hflip")
	}
	if p.Flip {
		picture = append(picture, "vflip")
	}
	if opts.MinHeight > 0 {
		// Enlarged before anything is drawn on it, so the timer and logo are
		// drawn sharp at the final size. A picture already tall enough keeps
		// its height, which makes this do nothing.
		picture = append(picture, fmt.Sprintf("scale=-2:'max(%d,ih)':flags=lanczos", opts.MinHeight))
	}
	if opts.Timer {
		drawn = append(drawn, timerFilter)
		_, from := lapLines(p)
		for i, at := range from {
			drawn = append(drawn, fmt.Sprintf(lapFilter, i+1, i, at))
		}
	}

	switch {
	case opts.Logo && logoW > 0:
		// The logo is a second input, laid over the picture.
		if len(picture) == 0 {
			picture = []string{"null"}
		}
		graph := "[0:v]" + strings.Join(picture, ",") + "[pic];" +
			fmt.Sprintf("[1:v]scale=%d:-2,", logoW) + logoOverlay
		if len(drawn) > 0 {
			graph += "," + strings.Join(drawn, ",")
		}
		args = append(args, "-i", logoFile, "-filter_complex", graph+"[out]", "-map", "[out]", "-map", "0:a?")
	default:
		args = append(args, "-map", "0:v:0", "-map", "0:a?")
		if filters := append(picture, drawn...); len(filters) > 0 {
			args = append(args, "-vf", strings.Join(filters, ","))
		}
	}
	return append(args,
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k",
		"-movflags", "+faststart", "-f", "mp4", out)
}

// findFFmpeg finds the ffmpeg program: the one named in the settings, else
// the one on the PATH, else one installed since this program was started,
// else one in FPVTrackside's folder, where Trackside looks for its own.
func findFFmpeg(configured, tracksideDir string) (string, error) {
	if configured != "" {
		if st, err := os.Stat(configured); err != nil || st.IsDir() {
			return "", fmt.Errorf("ffmpeg isn't at %s, where the settings say it is", configured)
		}
		return configured, nil
	}
	if path, err := exec.LookPath("ffmpeg"); err == nil {
		return path, nil
	}
	folders := append(currentPath(), wingetFolders(os.Getenv("LOCALAPPDATA"))...)
	folders = append(folders, filepath.Join(tracksideDir, "ffmpeg"), tracksideDir)
	if path := ffmpegIn(folders); path != "" {
		return path, nil
	}
	return "", errors.New("ffmpeg wasn't found")
}

// ffmpegIn is the first ffmpeg.exe in the folders, or "".
func ffmpegIn(folders []string) string {
	for _, dir := range folders {
		if dir == "" {
			continue
		}
		path := filepath.Join(dir, "ffmpeg.exe")
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path
		}
	}
	return ""
}

// currentPath is the PATH Windows would give a program started now: the
// user's own and then the computer's, as the registry has them. This
// program's PATH is the one from when it (or the installer that started it)
// was started, so an ffmpeg installed since is only on this one.
func currentPath() []string {
	var dirs []string
	for _, k := range []struct {
		root registry.Key
		path string
	}{
		{registry.CURRENT_USER, `Environment`},
		{registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`},
	} {
		key, err := registry.OpenKey(k.root, k.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		value, _, err := key.GetStringValue("Path")
		key.Close()
		if err != nil {
			continue
		}
		if expanded, err := registry.ExpandString(value); err == nil {
			value = expanded
		}
		dirs = append(dirs, filepath.SplitList(value)...)
	}
	return dirs
}

// wingetFolders are where winget (Windows' own installer of programs, which
// this program's installer uses) puts an ffmpeg: a link in its Links folder
// where it can make one, and the package itself. localAppData is the user's
// %LOCALAPPDATA%.
func wingetFolders(localAppData string) []string {
	if localAppData == "" {
		return nil
	}
	base := filepath.Join(localAppData, "Microsoft", "WinGet")
	dirs := []string{filepath.Join(base, "Links")}
	packages, _ := filepath.Glob(filepath.Join(base, "Packages", "Gyan.FFmpeg*", "*", "bin"))
	return append(dirs, packages...)
}

// cutClip runs ffmpeg to cut one clip. The clip is written under a temporary
// name and only given its own once it is complete. dir is the folder from
// overlayFolder, needed if anything is to be drawn on the clip, and logoW the
// width to draw the logo at, 0 for none.
func cutClip(ctx context.Context, ffmpeg string, p clipPlan, file string, opts clipOptions, dir string, logoW int) error {
	part := file + ".part"
	cmd := exec.CommandContext(ctx, ffmpeg, clipArgs(p, part, opts, logoW)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // no console window
	cmd.Dir = dir
	if opts.Timer {
		texts := map[string]string{timerTextFile: timerText(p.LapsAt, p.Run.Total)}
		laps, _ := lapLines(p)
		for i, line := range laps {
			texts[fmt.Sprintf(lapTextFile, i+1)] = line
		}
		for name, text := range texts {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
				return err
			}
		}
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		os.Remove(part)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
			return errors.New(last)
		}
		return err
	}
	if st, err := os.Stat(part); err != nil || st.Size() == 0 {
		os.Remove(part)
		return errors.New("ffmpeg made an empty file")
	}
	os.Remove(file)
	return os.Rename(part, file)
}

// exportClips cuts each plan's clip into folder, one after another, calling
// done as each finishes or fails. It stops early if ctx is cancelled. The
// error it returns is one that stopped it cutting any.
func exportClips(ctx context.Context, ffmpeg, folder, track string, plans []clipPlan, opts clipOptions, logo []byte, done func(i int, name string, err error)) error {
	dir := ""
	if opts.Timer || opts.Logo {
		made, err := overlayFolder(opts, logo)
		if err != nil {
			return err
		}
		defer os.RemoveAll(made)
		dir = made
	}
	sizes := map[string][2]int{} // of the videos, so the logo can be sized to them
	for i, p := range plans {
		if ctx.Err() != nil {
			return nil
		}
		logoW := 0
		if opts.Logo {
			size, known := sizes[p.Video]
			if !known {
				size[0], size[1], _ = videoSize(ctx, ffmpeg, p.Video)
				sizes[p.Video] = size
			}
			if size[0] > 0 && size[1] > 0 {
				logoW = logoWidth(p, opts, size[0], size[1])
			}
		}
		name := clipFileName(track, p.Run.Name, p.Run.Total)
		err := cutClip(ctx, ffmpeg, p, filepath.Join(folder, name), opts, dir, logoW)
		if ctx.Err() != nil {
			return nil
		}
		done(i, name, err)
	}
	return nil
}
