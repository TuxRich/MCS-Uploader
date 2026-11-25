package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/lxn/win"
)

const (
	exportTitle      = "Export Video"
	exportButtonText = "Export Video"
	stopButtonText   = "Stop Export"
)

// Only touched on the UI thread, like the rest of the window's state.
var (
	preparingExport bool               // the event's recordings are being looked over
	exportCancel    context.CancelFunc // stops the export that is running; nil if none is
)

// exportVideoTriggered is the Export Video button and menu item. While an
// export is running the button stops it instead.
func exportVideoTriggered() {
	if exportCancel != nil {
		exportCancel()
		return
	}
	openExport(false)
}

// exportAfterUpload opens the export window by itself once the loaded
// event's times are on MCS, so that its videos can follow them.
func exportAfterUpload() { openExport(true) }

// recordedClips counts the clips that have a recording to be cut from.
func recordedClips(plans []clipPlan) int {
	n := 0
	for _, p := range plans {
		if p.Problem == "" {
			n++
		}
	}
	return n
}

// openExport looks over the loaded event's recordings, shows which pilots'
// laps can be cut out of them, and cuts the ones chosen.
//
// unasked is that nobody pressed Export Video: the window is opening by
// itself, after the times were uploaded. It then opens only if a pilot's laps
// were recorded, and whatever else would stop an export goes unsaid or, at
// most, in the log: nobody asked, so nothing gets in their way.
func openExport(unasked bool) {
	if preparingExport || exportCancel != nil {
		return
	}
	warn := func(text string) {
		if !unasked {
			walk.MsgBox(mw, exportTitle, text, walk.MsgBoxIconWarning)
		}
	}
	if loadedID == "" {
		warn("Load an event first.")
		return
	}
	if cfg.TrackID == 0 || cfg.TrackName == "" {
		warn("Choose a track first, in the Track list at the top: its name goes in the videos' file names.")
		return
	}
	ffmpeg, ffmpegErr := findFFmpeg(cfg.FFmpegPath, tracksideDir)
	if ffmpegErr != nil && !unasked {
		warn(fmt.Sprintf("The videos are cut with a program called ffmpeg, and %v.\n\n"+
			"To install it, run this in a terminal and then start MCS Uploader again:\n\n    winget install ffmpeg\n\n"+
			"If you have it somewhere else, put the full path of ffmpeg.exe in mcs.json as \"ffmpeg_path\".", ffmpegErr))
		return
	}

	preparingExport = true
	dir, id, name := eventsDir, loadedID, loadedName
	tsDir, tsProfile := tracksideDir, tracksideProfile
	api, hasKey, timesUploads := cfg.api(), cfg.APIKey != "", cfg.uploadIDs(loadedID)
	go func() {
		// Its own reading of the event, so as not to share the live one.
		ev := newEventData(dir, id, name)
		_, readErr := ev.refresh()
		runs := consecutiveLapTotals(ev, 3)
		plans := planClips(ev, runs)
		if recorded := recordedClips(plans); unasked && (recorded == 0 || ffmpegErr != nil) {
			mw.Synchronize(func() {
				preparingExport = false
				if recorded > 0 {
					logf("Video: %d pilots' laps were recorded, but %v. Export Video says how to set it up.", recorded, ffmpegErr)
				}
			})
			return
		}
		records, known := tracksideRecords(tsDir, tsProfile)
		// Whether the videos can go on the league's YouTube channel as well.
		offer := checkVideoUpload(api, hasKey, timesUploads, runs)
		for i := range offer.Matches {
			plans[i].MCS = offer.Matches[i]
		}
		mw.Synchronize(func() {
			preparingExport = false
			if readErr != nil {
				logf("Reading the event: %v", readErr)
			}
			// Unasked, don't open on top of another of the program's windows
			// (the settings, say), or for an event loaded in the meantime.
			if unasked && (!win.IsWindowEnabled(mw.Handle()) || loadedID != id) {
				return
			}
			chooseAndExport(ffmpeg, api, plans, offer, known && !records)
		})
	}()
}

// chooseAndExport takes it from the clips that could be cut to cutting them:
// the window to choose pilots in, then the folder to save into.
// notRecording is that FPVTrackside isn't set to record races.
func chooseAndExport(ffmpeg string, api mcsAPI, plans []clipPlan, offer videoOffer, notRecording bool) {
	const howToRecord = "To record races, turn on \"Record Video For Replays\" for the video source in FPVTrackside's video settings."
	if len(plans) == 0 {
		walk.MsgBox(mw, exportTitle, "No pilot has 3 consecutive laps yet, so there are no clips to cut.", walk.MsgBoxIconInformation)
		return
	}
	problems := map[string]int{}
	for _, p := range plans {
		if p.Problem != "" {
			problems[p.Problem]++
		}
	}
	if recordedClips(plans) == 0 {
		text := "FPVTrackside isn't set to record races, and there is no recording of these pilots' laps.\n\n" + howToRecord
		if !notRecording {
			var lines []string
			for problem, n := range problems {
				lines = append(lines, fmt.Sprintf("%s (%d pilots)", problem, n))
			}
			sort.Strings(lines)
			text = "FPVTrackside is set to record races, but none of these pilots' laps has a recording that can be used:\n\n" +
				strings.Join(lines, "\n") +
				"\n\nTrackside only keeps so many videos (Videos To Keep, in its settings) and deletes the oldest."
		}
		walk.MsgBox(mw, exportTitle, text, walk.MsgBoxIconWarning)
		return
	}

	note := ""
	if notRecording {
		note = "FPVTrackside isn't set to record races at the moment, so races flown now won't have video. " + howToRecord
	}
	picked, opts, ok := chooseClips(plans, note, offer)
	if !ok {
		return
	}
	folder, ok := pickFolder("Choose the folder to save the videos into", cfg.VideoFolder)
	if !ok {
		return
	}
	cfg.VideoFolder, cfg.WholeFrame, cfg.VideoTimer, cfg.VideoLogo = folder, !opts.Crop, opts.Timer, opts.Logo
	if offer.Available {
		cfg.VideoUpload = opts.Upload // only a choice when it was on offer
	}
	saveCfg()
	opts.MinHeight = max(0, cfg.MinHeight)
	startExport(ffmpeg, api, folder, cfg.TrackName, picked, opts)
}

// exportModel is the list of pilots in the export window, each with a tick
// box. A pilot whose laps have no usable recording can't be ticked.
type exportModel struct {
	walk.TableModelBase
	plans   []clipPlan
	ticks   []bool
	upload  bool // the videos can be put on YouTube through MCS
	changed func()
}

func (m *exportModel) RowCount() int { return len(m.plans) }

func (m *exportModel) Value(row, col int) interface{} {
	if row < 0 || row >= len(m.plans) {
		return ""
	}
	p := m.plans[row]
	switch col {
	case 0:
		return p.Run.Name
	case 1:
		return secs(p.Run.Total)
	case 2:
		if p.Problem == "" {
			return fmt.Sprintf("%.1f s", p.Length)
		}
	case 3:
		if p.Problem != "" {
			return p.Problem
		}
		if !p.HasCrop {
			return "Recorded, but the pilot's own picture isn't marked: whole picture"
		}
		return "Recorded"
	case 4:
		switch {
		case !m.upload || p.Problem != "":
		case p.MCS.Why != "":
			return p.MCS.Why
		default:
			return "Can go on YouTube"
		}
	}
	return ""
}

func (m *exportModel) styleCell(style *walk.CellStyle) {
	row := style.Row()
	if row < 0 || row >= len(m.plans) {
		return
	}
	if m.plans[row].Problem != "" {
		style.TextColor = walk.RGB(140, 140, 140)
	}
}

func (m *exportModel) Checked(row int) bool { return m.ticks[row] }

func (m *exportModel) SetChecked(row int, checked bool) error {
	m.ticks[row] = checked && m.plans[row].Problem == ""
	if m.changed != nil {
		m.changed()
	}
	return nil
}

func (m *exportModel) ticked() int {
	n := 0
	for _, t := range m.ticks {
		if t {
			n++
		}
	}
	return n
}

// chooseClips shows the pilots whose laps can be exported, each with a tick
// box, and asks which to export and how. It returns the clips chosen, the
// choices made about them, and whether to go ahead.
func chooseClips(plans []clipPlan, note string, offer videoOffer) (picked []clipPlan, opts clipOptions, ok bool) {
	m := &exportModel{plans: plans, ticks: make([]bool, len(plans)), upload: offer.Available}
	recorded := 0
	for i, p := range plans {
		if p.Problem == "" {
			m.ticks[i] = true
			recorded++
		}
	}
	countText := func() string {
		text := fmt.Sprintf("%d of %d pilots ticked", m.ticked(), len(plans))
		if recorded < len(plans) {
			text += fmt.Sprintf("; %d have no recording of their laps", len(plans)-recorded)
		}
		return text
	}

	var dlg *walk.Dialog
	var countLbl *walk.Label
	var cropCB, timerCB, logoCB, uploadCB *walk.CheckBox
	var exportPB, cancelPB *walk.PushButton
	m.changed = func() {
		countLbl.SetText(countText())
		exportPB.SetEnabled(m.ticked() > 0)
	}

	// Putting the videos on YouTube, where that is on offer; and why not,
	// where it isn't.
	uploadText := "Put each video on the MCS YouTube channel, and its link against the pilot's time on MCS"
	uploadHint := "The videos go on the league's channel through the MCS site; no YouTube account is needed. Each club can upload 40 a day."
	if !offer.Available {
		uploadHint = "Not available: " + offer.Why
	}

	cmd, err := Dialog{
		AssignTo:      &dlg,
		Icon:          appIcon,
		Title:         exportTitle,
		DefaultButton: &exportPB,
		CancelButton:  &cancelPB,
		MinSize:       Size{Width: 960, Height: 620},
		Layout:        VBox{},
		Children: []Widget{
			Label{Text: "A video of each ticked pilot's 3 laps, with 3 seconds before and after, is cut from FPVTrackside's recording of the race."},
			Label{Visible: note != "", Text: note, TextColor: flagText},
			TableView{
				StretchFactor:    10,
				CheckBoxes:       true,
				ColumnsOrderable: false,
				Model:            m,
				StyleCell:        m.styleCell,
				Columns: []TableViewColumn{
					{Title: "Pilot", Width: 200},
					{Title: "3 laps", Width: 70, Alignment: AlignFar},
					{Title: "Video", Width: 70, Alignment: AlignFar},
					{Title: "Recording", Width: 290},
					{Title: "On MCS", Width: 280},
				},
			},
			Label{AssignTo: &countLbl, Text: countText()},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					CheckBox{AssignTo: &cropCB, Text: "Only the pilot's own picture (the recording has all the pilots' pictures side by side)", Checked: !cfg.WholeFrame},
					HSpacer{},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					CheckBox{AssignTo: &timerCB, Text: "Timer on the video, from the start of the first lap to the end of the third, with each lap's time under it as it is finished", Checked: cfg.VideoTimer},
					HSpacer{},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					CheckBox{AssignTo: &logoCB, Text: "MCS logo in the corner of the video", Checked: cfg.VideoLogo},
					HSpacer{},
				},
			},
			Label{
				Visible: cfg.MinHeight > 0,
				Text:    fmt.Sprintf("A video less than %d high is enlarged to %d, as video sites show small videos badly.", cfg.MinHeight, cfg.MinHeight),
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					CheckBox{AssignTo: &uploadCB, Text: uploadText, Enabled: offer.Available, Checked: offer.Available && cfg.VideoUpload},
					HSpacer{},
				},
			},
			Label{Text: uploadHint, TextColor: walk.RGB(110, 110, 110)},
			Label{Text: fmt.Sprintf("You are asked for the folder next. Files are named like:  %s", clipFileName(cfg.TrackName, "pilot name", plans[0].Run.Total))},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					HSpacer{},
					PushButton{AssignTo: &exportPB, Text: "Export...", Enabled: m.ticked() > 0, OnClicked: func() {
						opts = clipOptions{
							Crop: cropCB.Checked(), Timer: timerCB.Checked(), Logo: logoCB.Checked(),
							Upload: offer.Available && uploadCB.Checked(),
						}
						dlg.Accept()
					}},
					PushButton{AssignTo: &cancelPB, Text: "Cancel", OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
	}.Run(mw)
	if err != nil {
		logf("Export window failed: %v", err)
		return nil, clipOptions{}, false
	}
	if cmd != walk.DlgCmdOK {
		return nil, clipOptions{}, false
	}
	for i, p := range plans {
		if m.ticks[i] {
			picked = append(picked, p)
		}
	}
	return picked, opts, len(picked) > 0
}

// startExport cuts the clips in the background, saying in the log how it is
// going, and sums up when it is done. With the upload option each clip is
// put on YouTube through MCS as soon as it is cut.
func startExport(ffmpeg string, api mcsAPI, folder, track string, clips []clipPlan, opts clipOptions) {
	ctx, cancel := context.WithCancel(context.Background())
	exportCancel = cancel
	exportPB.SetText(stopButtonText)
	total := len(clips)
	logf("Video: cutting %d clips into %s", total, folder)

	made, failed := 0, 0                  // counted on the UI thread
	linked, notLinked, skipped := 0, 0, 0 // likewise: clips put on YouTube, or not
	logo := videoLogo()
	go func() {
		setupErr := exportClips(ctx, ffmpeg, folder, track, clips, opts, logo, func(i int, name string, err error) {
			mw.Synchronize(func() {
				if err != nil {
					failed++
					logf("Video: %d of %d failed, %s: %v", i+1, total, name, err)
					return
				}
				made++
				logf("Video: %d of %d  %s", i+1, total, name)
			})
			if err != nil || !opts.Upload {
				return
			}
			// Onto YouTube, and against the pilot's time on MCS. This runs
			// here, between clips, so one video is sent at a time.
			clip := clips[i]
			if clip.MCS.Why != "" || clip.MCS.ResultID == 0 {
				mw.Synchronize(func() {
					skipped++
					logf("YouTube: %s not uploaded. %s", clip.Run.Name, clip.MCS.Why)
				})
				return
			}
			mw.Synchronize(func() { logf("YouTube: uploading %s...", clip.Run.Name) })
			link, upErr := api.uploadVideo(ctx, clip.MCS.ResultID, filepath.Join(folder, name))
			if ctx.Err() != nil {
				return // stopped part-way; the summary says so
			}
			mw.Synchronize(func() {
				if upErr != nil {
					notLinked++
					logf("YouTube: %s could not be uploaded: %v", clip.Run.Name, upErr)
					return
				}
				linked++
				logf("YouTube: %s is at %s and against their time on MCS", clip.Run.Name, link)
			})
		})
		stopped := ctx.Err() != nil
		cancel()
		mw.Synchronize(func() {
			exportCancel = nil
			exportPB.SetText(exportButtonText)
			if setupErr != nil {
				logf("Video: %v", setupErr)
				walk.MsgBox(mw, exportTitle, fmt.Sprintf("No videos were made: %v.", setupErr), walk.MsgBoxIconWarning)
				return
			}
			text := fmt.Sprintf("%d of %d videos saved", made, total)
			if stopped {
				text += ", then stopped"
			}
			if failed > 0 {
				text += fmt.Sprintf("; %d couldn't be made (see the log at the bottom of the window)", failed)
			}
			logf("Video: %s", text)
			uploads := ""
			if opts.Upload {
				uploads = fmt.Sprintf("%d put on YouTube and against their times on MCS", linked)
				if notLinked > 0 {
					uploads += fmt.Sprintf("; %d couldn't be uploaded (see the log)", notLinked)
				}
				if skipped > 0 {
					uploads += fmt.Sprintf("; %d left out, for the reason in the On MCS column", skipped)
				}
				logf("YouTube: %s", uploads)
				uploads = "\n\n" + uploads + "."
			}
			if made == 0 {
				walk.MsgBox(mw, exportTitle, text+".", walk.MsgBoxIconWarning)
				return
			}
			if walk.MsgBox(mw, exportTitle, fmt.Sprintf("%s in\n%s%s\n\nOpen the folder?", text, folder, uploads),
				walk.MsgBoxIconInformation|walk.MsgBoxYesNo) == walk.DlgCmdYes {
				if err := exec.Command("explorer", folder).Start(); err != nil {
					logf("Couldn't open %s: %v", folder, err)
				}
			}
		})
	}()
}
