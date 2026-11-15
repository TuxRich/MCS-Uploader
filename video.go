package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
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
	Timer bool // a timer on the picture, running over the three laps
}

// The timer is drawn by ffmpeg from two files it finds in the folder it is
// run in (see timerFolder), so that no path has to be written into a filter,
// where colons, quotes and backslashes all mean something.
const (
	timerFontFile = "font.ttf"
	timerTextFile = "timer.txt"
	// Top right, where pilots' on-screen displays tend to have nothing; white
	// on a dark box so it reads over any picture; sized to the picture.
	timerFilter = "drawtext=fontfile=" + timerFontFile + ":textfile=" + timerTextFile +
		":fontsize=h/11:fontcolor=white:box=1:boxcolor=black@0.55:boxborderw=6:x=w-text_w-w/30:y=h/30"
)

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

// timerFolder makes a folder for ffmpeg to run in when it draws timers, with
// a font in it. A font whose digits are all one width, so the timer doesn't
// shake as it counts.
func timerFolder() (string, error) {
	fonts := filepath.Join(os.Getenv("WINDIR"), "Fonts")
	var font []byte
	for _, name := range []string{"consola.ttf", "lucon.ttf", "cour.ttf", "arial.ttf"} {
		if data, err := os.ReadFile(filepath.Join(fonts, name)); err == nil {
			font = data
			break
		}
	}
	if font == nil {
		return "", fmt.Errorf("no font for the timer was found in %s", fonts)
	}
	dir, err := os.MkdirTemp("", "mcs-uploader-")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, timerFontFile), font, 0600); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// clipArgs are ffmpeg's arguments for cutting a plan's clip into the file
// out.
//
// The clip is encoded afresh rather than copied: a copy can only start on a
// key frame, seconds away from where it should, and can't be cropped or
// drawn on.
func clipArgs(p clipPlan, out string, opts clipOptions) []string {
	secs := func(s float64) string { return strconv.FormatFloat(s, 'f', 3, 64) }
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-ss", secs(p.Start), "-t", secs(p.Length), "-i", p.Video,
		"-map", "0:v:0", "-map", "0:a?",
	}
	var filters []string
	if opts.Crop && p.HasCrop {
		filters = append(filters, cropFilter(p.Crop))
	}
	if p.Mirror {
		filters = append(filters, "hflip")
	}
	if p.Flip {
		filters = append(filters, "vflip")
	}
	if opts.Timer {
		filters = append(filters, timerFilter) // last, so it isn't cropped or turned over
	}
	if len(filters) > 0 {
		args = append(args, "-vf", strings.Join(filters, ","))
	}
	return append(args,
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k",
		"-movflags", "+faststart", "-f", "mp4", out)
}

// findFFmpeg finds the ffmpeg program: the one named in the settings, else
// the one on the PATH, else one in FPVTrackside's folder, where Trackside
// looks for its own.
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
	for _, name := range []string{filepath.Join("ffmpeg", "ffmpeg.exe"), "ffmpeg.exe"} {
		path := filepath.Join(tracksideDir, name)
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path, nil
		}
	}
	return "", errors.New("ffmpeg wasn't found")
}

// cutClip runs ffmpeg to cut one clip. The clip is written under a temporary
// name and only given its own once it is complete. timerDir is the folder
// from timerFolder, needed if the clip is to have a timer.
func cutClip(ctx context.Context, ffmpeg string, p clipPlan, file string, opts clipOptions, timerDir string) error {
	part := file + ".part"
	cmd := exec.CommandContext(ctx, ffmpeg, clipArgs(p, part, opts)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // no console window
	if opts.Timer {
		if err := os.WriteFile(filepath.Join(timerDir, timerTextFile), []byte(timerText(p.LapsAt, p.Run.Total)), 0600); err != nil {
			return err
		}
		cmd.Dir = timerDir
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
func exportClips(ctx context.Context, ffmpeg, folder, track string, plans []clipPlan, opts clipOptions, done func(i int, name string, err error)) error {
	timerDir := ""
	if opts.Timer {
		dir, err := timerFolder()
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		timerDir = dir
	}
	for i, p := range plans {
		if ctx.Err() != nil {
			return nil
		}
		name := clipFileName(track, p.Run.Name, p.Run.Total)
		err := cutClip(ctx, ffmpeg, p, filepath.Join(folder, name), opts, timerDir)
		if ctx.Err() != nil {
			return nil
		}
		done(i, name, err)
	}
	return nil
}
