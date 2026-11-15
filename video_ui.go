package main

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
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

// exportVideoTriggered is the Export Video button and menu item. It looks
// over the loaded event's recordings, shows which pilots' laps can be cut out
// of them, and cuts the ones chosen. While an export is running the button
// stops it instead.
func exportVideoTriggered() {
	if exportCancel != nil {
		exportCancel()
		return
	}
	if preparingExport {
		return
	}
	if loadedID == "" {
		walk.MsgBox(mw, exportTitle, "Load an event first.", walk.MsgBoxIconWarning)
		return
	}
	if cfg.TrackID == 0 || cfg.TrackName == "" {
		walk.MsgBox(mw, exportTitle, "Choose a track first, in the Track list at the top: its name goes in the videos' file names.", walk.MsgBoxIconWarning)
		return
	}
	ffmpeg, err := findFFmpeg(cfg.FFmpegPath, tracksideDir)
	if err != nil {
		walk.MsgBox(mw, exportTitle,
			fmt.Sprintf("The videos are cut with a program called ffmpeg, and %v.\n\n"+
				"To install it, run this in a terminal and then start MCS Uploader again:\n\n    winget install ffmpeg\n\n"+
				"If you have it somewhere else, put the full path of ffmpeg.exe in mcs.json as \"ffmpeg_path\".", err),
			walk.MsgBoxIconWarning)
		return
	}

	preparingExport = true
	dir, id, name := eventsDir, loadedID, loadedName
	tsDir, tsProfile := tracksideDir, tracksideProfile
	go func() {
		// Its own reading of the event, so as not to share the live one.
		ev := newEventData(dir, id, name)
		_, readErr := ev.refresh()
		plans := planClips(ev, consecutiveLapTotals(ev, 3))
		records, known := tracksideRecords(tsDir, tsProfile)
		mw.Synchronize(func() {
			preparingExport = false
			if readErr != nil {
				logf("Reading the event: %v", readErr)
			}
			chooseAndExport(ffmpeg, plans, known && !records)
		})
	}()
}

// chooseAndExport takes it from the clips that could be cut to cutting them:
// the window to choose pilots in, then the folder to save into.
// notRecording is that FPVTrackside isn't set to record races.
func chooseAndExport(ffmpeg string, plans []clipPlan, notRecording bool) {
	const howToRecord = "To record races, turn on \"Record Video For Replays\" for the video source in FPVTrackside's video settings."
	if len(plans) == 0 {
		walk.MsgBox(mw, exportTitle, "No pilot has 3 consecutive laps yet, so there are no clips to cut.", walk.MsgBoxIconInformation)
		return
	}
	ready, problems := 0, map[string]int{}
	for _, p := range plans {
		if p.Problem == "" {
			ready++
		} else {
			problems[p.Problem]++
		}
	}
	if ready == 0 {
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
	picked, crop, ok := chooseClips(plans, note)
	if !ok {
		return
	}
	folder, ok := pickFolder("Choose the folder to save the videos into", cfg.VideoFolder)
	if !ok {
		return
	}
	cfg.VideoFolder, cfg.WholeFrame = folder, !crop
	saveCfg()
	startExport(ffmpeg, folder, cfg.TrackName, picked, crop)
}

// exportModel is the list of pilots in the export window, each with a tick
// box. A pilot whose laps have no usable recording can't be ticked.
type exportModel struct {
	walk.TableModelBase
	plans   []clipPlan
	ticks   []bool
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
// box, and asks which to export. It returns the clips chosen, whether to keep
// only each pilot's own picture, and whether to go ahead.
func chooseClips(plans []clipPlan, note string) (picked []clipPlan, crop, ok bool) {
	m := &exportModel{plans: plans, ticks: make([]bool, len(plans))}
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
	var cropCB *walk.CheckBox
	var exportPB, cancelPB *walk.PushButton
	m.changed = func() {
		countLbl.SetText(countText())
		exportPB.SetEnabled(m.ticked() > 0)
	}

	cmd, err := Dialog{
		AssignTo:      &dlg,
		Title:         exportTitle,
		DefaultButton: &exportPB,
		CancelButton:  &cancelPB,
		MinSize:       Size{Width: 760, Height: 560},
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
					{Title: "Recording", Width: 360},
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
			Label{Text: fmt.Sprintf("You are asked for the folder next. Files are named like:  %s", clipFileName(cfg.TrackName, "pilot name", plans[0].Run.Total))},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					HSpacer{},
					PushButton{AssignTo: &exportPB, Text: "Export...", Enabled: m.ticked() > 0, OnClicked: func() {
						crop = cropCB.Checked()
						dlg.Accept()
					}},
					PushButton{AssignTo: &cancelPB, Text: "Cancel", OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
	}.Run(mw)
	if err != nil {
		logf("Export window failed: %v", err)
		return nil, false, false
	}
	if cmd != walk.DlgCmdOK {
		return nil, false, false
	}
	for i, p := range plans {
		if m.ticks[i] {
			picked = append(picked, p)
		}
	}
	return picked, crop, len(picked) > 0
}

// startExport cuts the clips in the background, saying in the log how it is
// going, and sums up when it is done.
func startExport(ffmpeg, folder, track string, clips []clipPlan, crop bool) {
	ctx, cancel := context.WithCancel(context.Background())
	exportCancel = cancel
	exportPB.SetText(stopButtonText)
	total := len(clips)
	logf("Video: cutting %d clips into %s", total, folder)

	made, failed := 0, 0 // counted on the UI thread
	go func() {
		exportClips(ctx, ffmpeg, folder, track, clips, crop, func(i int, name string, err error) {
			mw.Synchronize(func() {
				if err != nil {
					failed++
					logf("Video: %d of %d failed, %s: %v", i+1, total, name, err)
					return
				}
				made++
				logf("Video: %d of %d  %s", i+1, total, name)
			})
		})
		stopped := ctx.Err() != nil
		cancel()
		mw.Synchronize(func() {
			exportCancel = nil
			exportPB.SetText(exportButtonText)
			text := fmt.Sprintf("%d of %d videos saved", made, total)
			if stopped {
				text += ", then stopped"
			}
			if failed > 0 {
				text += fmt.Sprintf("; %d couldn't be made (see the log at the bottom of the window)", failed)
			}
			logf("Video: %s", text)
			if made == 0 {
				walk.MsgBox(mw, exportTitle, text+".", walk.MsgBoxIconWarning)
				return
			}
			if walk.MsgBox(mw, exportTitle, fmt.Sprintf("%s in\n%s\n\nOpen the folder?", text, folder),
				walk.MsgBoxIconInformation|walk.MsgBoxYesNo) == walk.DlgCmdYes {
				if err := exec.Command("explorer", folder).Start(); err != nil {
					logf("Couldn't open %s: %v", folder, err)
				}
			}
		})
	}()
}
