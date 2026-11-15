package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
)

const appTitle = "MCS Uploader"

// pollInterval is how often a loaded event's folder is checked for changes.
const pollInterval = 3 * time.Second

// Everything below is only touched on the UI thread. Background work (the
// poll of the event folder, calls to MCS) hands its results back through
// mw.Synchronize.
var (
	mw          *walk.MainWindow
	eventCB     *walk.ComboBox
	trackCB     *walk.ComboBox
	tableTV     *walk.TableView
	logTE       *walk.TextEdit
	statusLbl   *walk.Label
	showBoardAc *walk.Action
	clubOnlyAc  *walk.Action
	exportPB    *walk.PushButton
	model       = &boardModel{}

	cfg     settings
	cfgFile string

	tracksideDir     string // FPVTrackside's own folder
	tracksideProfile string // the profile it has loaded
	eventsDir        string
	events           []eventListing

	loadedID, loadedName string
	stopPoll             chan struct{}
	liveRuns             []lapRun // the loaded event's 3-lap leaderboard
	liveRows             []mcsRow // the same, as it would be uploaded
	liveAt               time.Time

	baseline   []mcsEntry // the track's MCS leaderboard, all clubs, as last downloaded
	baselineOK bool
	baselineAt time.Time
	fetching   bool

	meKnown    bool // the API key's club has been looked up
	myClubID   int64
	myClubName string

	listedTracks  []mcsTrack // the tracks MCS lists, as last downloaded
	offeredTracks []mcsTrack // what the track list in the window offers
	fillingTracks bool       // the track list is being filled, not chosen from
)

// --- The times table ---

const (
	colPos = iota
	colMove
	colPilot
	colClub
	colTime
	colLap1
	colLap2
	colLap3
	colBestLap
	colRounds
	colLaps
	colCheck
)

type boardModel struct {
	walk.TableModelBase
	rows []boardRow
}

func (m *boardModel) RowCount() int { return len(m.rows) }

func (m *boardModel) Value(row, col int) interface{} {
	if row < 0 || row >= len(m.rows) {
		return ""
	}
	r := m.rows[row]
	// The laps shown are those of the time shown: this event's run, unless
	// the pilot's MCS time is still the better one.
	run := r.Run
	if run != nil && run.Total != r.Time {
		run = nil
	}
	switch col {
	case colPos:
		return fmt.Sprint(r.Pos)
	case colMove:
		switch {
		case r.New:
			return "NEW"
		case r.Move > 0:
			return fmt.Sprintf("▲ %d", r.Move)
		case r.Move < 0:
			return fmt.Sprintf("▼ %d", -r.Move)
		}
	case colPilot:
		return r.Pilot
	case colClub:
		return r.Club
	case colTime:
		return secs(r.Time)
	case colLap1:
		if run != nil {
			return secs(run.Lap1)
		}
	case colLap2:
		if run != nil {
			return secs(run.Lap2)
		}
	case colLap3:
		if run != nil {
			return secs(run.Lap3)
		}
	case colBestLap:
		return secs(r.BestLap)
	case colRounds:
		if r.Run != nil {
			return fmt.Sprint(r.Run.RoundsFlown)
		}
	case colLaps:
		if r.Run != nil {
			return fmt.Sprint(r.Run.LapsFlown)
		}
	case colCheck:
		return r.Check.String()
	}
	return ""
}

func secs(s float64) string {
	if s <= 0 {
		return ""
	}
	return fmt.Sprintf("%.3f", s)
}

var (
	liveBG   = walk.RGB(255, 247, 200) // pilots flying in this event
	upText   = walk.RGB(0, 135, 0)
	dnText   = walk.RGB(190, 40, 40)
	newText  = walk.RGB(0, 90, 200)
	flagText = walk.RGB(205, 85, 0) // laps that look wrong
)

func styleCell(style *walk.CellStyle) {
	row := style.Row()
	if row < 0 || row >= len(model.rows) {
		return
	}
	r := model.rows[row]
	board := showingBoard()
	if board && r.Live {
		style.BackgroundColor = liveBG
	}
	switch col := style.Col(); col {
	case colMove:
		switch {
		case !board:
		case r.New:
			style.TextColor = newText
		case r.Move > 0:
			style.TextColor = upText
		case r.Move < 0:
			style.TextColor = dnText
		}
	case colTime:
		if board && r.Better {
			style.TextColor = upText
		}
	case colLap1, colLap2, colLap3:
		if r.Check.Lap(col - colLap1 + 1) {
			style.TextColor = flagText
		}
	case colBestLap:
		if r.Check.BestUnder {
			style.TextColor = flagText
		}
	case colCheck:
		style.TextColor = flagText
	}
}

// showingBoard reports whether the table is the track's MCS leaderboard with
// the event's times in it, rather than the event on its own.
func showingBoard() bool { return cfg.ShowBoard && baselineOK }

// clubForBoard is the club the event's pilots fly for on MCS: the one chosen
// in the settings (admin keys), else the API key's own.
func clubForBoard() (int64, string) {
	if cfg.ClubID != 0 {
		return cfg.ClubID, cfg.ClubName
	}
	return myClubID, myClubName
}

// rebuild redraws the table and status line from what is currently known.
func rebuild() {
	board := showingBoard()
	if board {
		club, clubName := clubForBoard()
		model.rows = mergeBoard(baseline, liveRuns, club, clubName, cfg.ClubOnly && club != 0)
	} else {
		model.rows = eventBoard(liveRuns)
	}
	flagged := 0
	for i := range model.rows {
		if run := model.rows[i].Run; run != nil {
			model.rows[i].Check = checkLaps(run.Lap1, run.Lap2, run.Lap3, run.BestLap, cfg.FlagLapUnder)
			if model.rows[i].Check.Flagged() {
				flagged++
			}
		}
	}
	model.PublishRowsReset()
	// A reset only tells the table how many rows there are now; rows already
	// on screen keep what they showed unless it is told to draw them again.
	tableTV.Invalidate()
	cols := tableTV.Columns()
	cols.At(colMove).SetVisible(board)
	cols.At(colClub).SetVisible(board)

	var parts []string
	if loadedID == "" {
		parts = append(parts, "No event loaded")
	} else {
		pilots := fmt.Sprintf("%d pilots with 3 laps", len(liveRuns))
		if flagged > 0 {
			pilots += fmt.Sprintf(", %d to check", flagged)
		}
		parts = append(parts, fmt.Sprintf("Event: %s (%s)", loadedName, pilots))
		if !liveAt.IsZero() {
			parts = append(parts, "updated "+liveAt.Format("15:04:05"))
		}
	}
	if board {
		scope := "all clubs"
		if club, name := clubForBoard(); cfg.ClubOnly && club != 0 {
			scope = name + " only"
		}
		parts = append(parts, fmt.Sprintf("MCS times from %s, %s", baselineAt.Format("15:04"), scope))
	}
	statusLbl.SetText(strings.Join(parts, "   ·   "))
}

func logf(format string, args ...any) {
	logTE.AppendText(time.Now().Format("15:04:05 ") + fmt.Sprintf(format, args...) + "\r\n")
}

func saveCfg() {
	if err := cfg.save(cfgFile); err != nil {
		logf("Couldn't save %s: %v", cfgFile, err)
	}
}

// --- Events ---

// findEvents fills the event list from FPVTrackside's events folder, or from
// the folder chosen instead. If the folder can't be opened it says so and
// offers to pick another.
func findEvents() {
	tracksideDir = filepath.Join(os.Getenv("LOCALAPPDATA"), "FPVTrackside")
	folder, profile := eventsFolder(tracksideDir)
	tracksideProfile = profile
	source := fmt.Sprintf("FPVTrackside profile %q", profile)
	if cfg.EventsFolder != "" {
		folder, source = cfg.EventsFolder, "chosen folder"
	}
	for {
		list, err := listEvents(folder)
		if err == nil {
			eventsDir, events = folder, list
			labels := make([]string, len(list))
			for i, e := range list {
				labels[i] = e.Label
			}
			eventCB.SetModel(labels)
			if len(labels) > 0 {
				eventCB.SetCurrentIndex(0)
			}
			logf("Events folder (%s): %s, %d events", source, folder, len(list))
			return
		}
		if walk.MsgBox(mw, appTitle,
			fmt.Sprintf("The events folder can't be opened:\n\n%s\n%v\n\n"+
				"Check the folder is there (or the drive is connected), or the Event Storage Location in FPVTrackside's settings.\n\n"+
				"Choose the events folder yourself?", folder, err),
			walk.MsgBoxIconWarning|walk.MsgBoxYesNo) != walk.DlgCmdYes {
			logf("Events folder not found: %s", folder)
			return
		}
		picked, ok := pickFolder("Choose FPVTrackside's events folder", folder)
		if !ok {
			return
		}
		folder, source = picked, "chosen folder"
		cfg.EventsFolder = picked
		saveCfg()
	}
}

// pickFolder asks for a folder, starting at start.
func pickFolder(title, start string) (string, bool) {
	folder, ok, err := chooseFolder(mw, title, start)
	if err == nil {
		return folder, ok
	}
	// Windows' folder chooser wouldn't open: fall back on the plainer one.
	logf("Folder chooser: %v", err)
	dlg := new(walk.FileDialog)
	dlg.Title = title
	ok, err = dlg.ShowBrowseFolder(mw)
	if err != nil || !ok || dlg.FilePath == "" {
		return "", false
	}
	return dlg.FilePath, true
}

// chooseEventsFolder is the menu item: use a different events folder, or go
// back to FPVTrackside's own by cancelling out of a failed one.
func chooseEventsFolder() {
	picked, ok := pickFolder("Choose FPVTrackside's events folder", eventsDir)
	if !ok {
		return
	}
	cfg.EventsFolder = picked
	saveCfg()
	findEvents()
}

func useTracksideFolder() {
	cfg.EventsFolder = ""
	saveCfg()
	findEvents()
}

// loadEvent starts following the event chosen in the list.
func loadEvent() {
	i := eventCB.CurrentIndex()
	if i < 0 || i >= len(events) {
		return
	}
	if stopPoll != nil {
		close(stopPoll)
	}
	stop := make(chan struct{})
	stopPoll = stop
	loadedID, loadedName = events[i].ID, events[i].Name
	liveRuns, liveRows, liveAt = nil, nil, time.Time{}
	mw.SetTitle(appTitle + " - " + loadedName)
	logf("Loading %s", events[i].Label)
	rebuild()
	go poll(newEventData(eventsDir, events[i].ID, events[i].Name), stop)
	if cfg.ShowBoard && cfg.TrackID != 0 {
		fetchBaseline()
	}
}

// poll follows an event's folder until stop is closed, handing the times to
// the window whenever something has changed.
func poll(ev *eventData, stop chan struct{}) {
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	first, lastErr := true, ""
	for {
		changed, err := ev.refresh()
		errText := ""
		if err != nil {
			errText = err.Error()
		}
		if changed || first || errText != lastErr {
			runs := consecutiveLapTotals(ev, 3) // MCS leaderboards are best 3 consecutive laps
			rows := mcsRows(ev, runs)
			report := errText != lastErr && errText != ""
			mw.Synchronize(func() {
				select {
				case <-stop:
					return // another event has been loaded since
				default:
				}
				if report {
					logf("Reading the event: %s", errText)
				}
				liveRuns, liveRows, liveAt = runs, rows, time.Now()
				rebuild()
			})
		}
		first, lastErr = false, errText
		select {
		case <-stop:
			return
		case <-tick.C:
		}
	}
}

// --- MCS ---

// showTracks fills the track list in the window and selects the chosen track.
func showTracks() {
	choices, current := trackChoices(listedTracks, cfg.TrackID, cfg.TrackName)
	names := make([]string, len(choices))
	for i, t := range choices {
		names[i] = t.Name
	}
	fillingTracks = true
	offeredTracks = choices
	trackCB.SetModel(names)
	trackCB.SetCurrentIndex(current)
	fillingTracks = false
}

// loadTracks downloads the list of tracks from MCS for the track list.
func loadTracks() {
	api := cfg.api()
	go func() {
		list, err := api.tracks()
		mw.Synchronize(func() {
			if err != nil {
				logf("MCS: couldn't get the list of tracks: %v", err)
				return
			}
			gotTracks(list)
		})
	}()
}

// gotTracks takes a freshly downloaded list of tracks: it fills the track
// list in the window, and picks up the chosen track's name if that has been
// changed on MCS.
func gotTracks(list []mcsTrack) {
	listedTracks = list
	for _, t := range list {
		if t.ID == cfg.TrackID && t.Name != cfg.TrackName {
			cfg.TrackName = t.Name
			saveCfg()
		}
	}
	showTracks()
}

// trackChosen is the track list in the window being changed.
func trackChosen() {
	if fillingTracks {
		return
	}
	i := trackCB.CurrentIndex()
	if i < 0 || i >= len(offeredTracks) || offeredTracks[i].ID == cfg.TrackID {
		return
	}
	setTrack(offeredTracks[i])
}

// setTrack changes the track times are uploaded to and compared against.
func setTrack(t mcsTrack) {
	cfg.TrackID, cfg.TrackName = t.ID, t.Name
	saveCfg()
	baseline, baselineOK = nil, false // the old track's times
	logf("MCS: track set to %q", t.Name)
	if cfg.ShowBoard {
		fetchBaseline()
	}
	rebuild()
}

// askForAPIKey says the API key hasn't been set up and opens the settings so
// it can be.
func askForAPIKey() {
	walk.MsgBox(mw, appTitle,
		"Your MCS API key hasn't been set up yet, and times can't be uploaded without it.\n\n"+
			"The settings will open next. Paste your key in there; the link under it takes you to the page on the MCS site where you get one.",
		walk.MsgBoxIconInformation)
	showSettings()
}

// openInBrowser opens a web page in the default browser.
func openInBrowser(url string) {
	if err := exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start(); err != nil {
		logf("Couldn't open %s: %v", url, err)
	}
}

// fetchBaseline downloads the chosen track's MCS leaderboard, and looks up
// which club the API key belongs to if that isn't known yet.
func fetchBaseline() {
	if cfg.TrackID == 0 {
		walk.MsgBox(mw, appTitle, "Choose a track first, in the Track list at the top.", walk.MsgBoxIconWarning)
		return
	}
	if fetching {
		return
	}
	fetching = true
	api, trackID, trackName := cfg.api(), cfg.TrackID, cfg.TrackName
	needMe := !meKnown && cfg.APIKey != ""
	go func() {
		var me mcsMe
		var meErr error
		if needMe {
			me, meErr = api.me()
		}
		board, err := api.leaderboard(trackID)
		mw.Synchronize(func() {
			fetching = false
			if needMe {
				if meErr != nil {
					logf("MCS: couldn't check the API key: %v", meErr)
				} else {
					meKnown, myClubID, myClubName = true, me.ClubID, me.Club
				}
			}
			if trackID != cfg.TrackID {
				// The track was changed while this was downloading.
				if cfg.ShowBoard && cfg.TrackID != 0 {
					fetchBaseline()
				}
				return
			}
			if err != nil {
				logf("MCS: couldn't download the times for %q: %v", trackName, err)
				return
			}
			baseline, baselineOK, baselineAt = board, true, time.Now()
			logf("MCS: downloaded %d times for %q", len(board), trackName)
			if club, _ := clubForBoard(); club == 0 {
				logf("MCS: your club isn't known (no API key set), so pilots are matched by name alone and \"My club only\" has no effect")
			}
			rebuild()
		})
	}()
}

func toggleShowBoard() {
	cfg.ShowBoard = !cfg.ShowBoard
	showBoardAc.SetChecked(cfg.ShowBoard)
	saveCfg()
	if cfg.ShowBoard && !baselineOK {
		fetchBaseline()
	}
	rebuild()
}

func toggleClubOnly() {
	cfg.ClubOnly = !cfg.ClubOnly
	clubOnlyAc.SetChecked(cfg.ClubOnly)
	saveCfg()
	rebuild()
}

// uploadTriggered sends the loaded event's times to MCS, replacing the
// event's previous upload if there was one.
func uploadTriggered() {
	if cfg.APIKey == "" {
		askForAPIKey()
		return
	}
	if cfg.TrackID == 0 {
		walk.MsgBox(mw, "Upload to MCS", "Choose a track first, in the Track list at the top.", walk.MsgBoxIconWarning)
		return
	}
	if loadedID == "" {
		walk.MsgBox(mw, "Upload to MCS", "Load an event first.", walk.MsgBoxIconWarning)
		return
	}
	if len(liveRows) == 0 {
		walk.MsgBox(mw, "Upload to MCS", "No pilot has 3 consecutive laps yet, so there is nothing to upload.", walk.MsgBoxIconInformation)
		return
	}
	api, trackID, trackName, clubID := cfg.api(), cfg.TrackID, cfg.TrackName, cfg.ClubID
	eventID, previous := loadedID, cfg.Uploads[loadedID]

	// What is confirmed is what is sent, even if times change meanwhile.
	rows, skipped, ok := confirmUpload(liveRows, trackName, previous != 0, cfg.Skipped[eventID])
	if !ok {
		return
	}
	if len(skipped) == 0 {
		delete(cfg.Skipped, eventID)
	} else {
		cfg.Skipped[eventID] = skipped
	}
	saveCfg()
	if len(skipped) == 0 {
		logf("MCS: uploading %d pilots to %q...", len(rows), trackName)
	} else {
		logf("MCS: uploading %d pilots to %q, %d left out...", len(rows), trackName, len(skipped))
	}
	go func() {
		id, imported, err := api.upload(trackID, clubID, rows, previous)
		mw.Synchronize(func() {
			if err != nil {
				logf("MCS: upload failed: %v", err)
				return
			}
			cfg.Uploads[eventID] = id
			saveCfg()
			logf("MCS: uploaded %d pilots to track %q (upload %d)", imported, trackName, id)
		})
	}()
}

// uploadModel is the list of times in the upload confirmation, each with a
// tick box.
type uploadModel struct {
	walk.TableModelBase
	rows    []mcsRow
	ticks   []bool
	checks  []lapCheck // what looks wrong about each row's laps
	changed func()
}

func (m *uploadModel) RowCount() int { return len(m.rows) }

func (m *uploadModel) Value(row, col int) interface{} {
	if row < 0 || row >= len(m.rows) {
		return ""
	}
	r := m.rows[row]
	switch col {
	case 0:
		return r.Pilot
	case 1:
		return secs(r.Time)
	case 2:
		return secs(r.Lap1)
	case 3:
		return secs(r.Lap2)
	case 4:
		return secs(r.Lap3)
	case 5:
		return secs(r.BestLap)
	case 6:
		return m.checks[row].String()
	}
	return ""
}

func (m *uploadModel) styleCell(style *walk.CellStyle) {
	row := style.Row()
	if row < 0 || row >= len(m.checks) {
		return
	}
	c := m.checks[row]
	switch col := style.Col(); col {
	case 2, 3, 4:
		if c.Lap(col - 1) {
			style.TextColor = flagText
		}
	case 5:
		if c.BestUnder {
			style.TextColor = flagText
		}
	case 6:
		style.TextColor = flagText
	}
}

func (m *uploadModel) Checked(row int) bool { return m.ticks[row] }

func (m *uploadModel) SetChecked(row int, checked bool) error {
	m.ticks[row] = checked
	if m.changed != nil {
		m.changed()
	}
	return nil
}

func (m *uploadModel) ticked() int {
	n := 0
	for _, t := range m.ticks {
		if t {
			n++
		}
	}
	return n
}

// confirmUpload shows the times about to be uploaded, each with a tick box,
// and asks whether to go ahead. Pilots in skipped, those left out of the
// event's upload last time, start unticked. It returns the rows to upload and
// the pilots left out, and whether the upload was confirmed.
func confirmUpload(rows []mcsRow, trackName string, replacing bool, skipped []string) (upload []mcsRow, nowSkipped []string, ok bool) {
	m := &uploadModel{rows: rows, ticks: uploadTicks(rows, skipped), checks: make([]lapCheck, len(rows))}
	for i, r := range rows {
		m.checks[i] = checkLaps(r.Lap1, r.Lap2, r.Lap3, r.BestLap, cfg.FlagLapUnder)
	}
	countText := func() string {
		text := fmt.Sprintf("%d of %d pilots ticked", m.ticked(), len(rows))
		// Only the ticked ones matter: they are what will be uploaded.
		toCheck := 0
		for i, c := range m.checks {
			if m.ticks[i] && c.Flagged() {
				toCheck++
			}
		}
		if toCheck > 0 {
			text += fmt.Sprintf(", %d of them with laps to check (see the Check column)", toCheck)
		}
		return text
	}

	var dlg *walk.Dialog
	var countLbl *walk.Label
	var uploadPB, cancelPB *walk.PushButton
	m.changed = func() {
		countLbl.SetText(countText())
		uploadPB.SetEnabled(m.ticked() > 0)
	}

	intro := fmt.Sprintf("These times will be uploaded to %q. Untick a pilot to leave their time out.", trackName)
	dvr := fmt.Sprintf(`You can add links to DVR afterwards, on the <a href="%s">DVR page of the MCS site</a>.`, cfg.api().dvrPage())

	cmd, err := Dialog{
		AssignTo:      &dlg,
		Title:         "Upload to MCS",
		DefaultButton: &uploadPB,
		CancelButton:  &cancelPB,
		MinSize:       Size{Width: 890, Height: 560},
		Layout:        VBox{},
		Children: []Widget{
			Label{Text: intro},
			TableView{
				StretchFactor:    10,
				CheckBoxes:       true,
				ColumnsOrderable: false,
				Model:            m,
				StyleCell:        m.styleCell,
				Columns: []TableViewColumn{
					{Title: "Pilot", Width: 200},
					{Title: "3 laps", Width: 70, Alignment: AlignFar},
					{Title: "Lap 1", Width: 60, Alignment: AlignFar},
					{Title: "Lap 2", Width: 60, Alignment: AlignFar},
					{Title: "Lap 3", Width: 60, Alignment: AlignFar},
					{Title: "Best lap", Width: 65, Alignment: AlignFar},
					{Title: "Check", Width: 300},
				},
			},
			Label{AssignTo: &countLbl, Text: countText()},
			Label{
				Visible: replacing,
				Text:    "This replaces the event's previous upload, so a pilot unticked now is removed from it.",
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					// A link label sizes itself for its maximum width; see showSettings.
					LinkLabel{Text: dvr, MaxSize: Size{Width: 520}, OnLinkActivated: func(link *walk.LinkLabelLink) { openInBrowser(link.URL()) }},
					HSpacer{},
				},
			},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					HSpacer{},
					PushButton{AssignTo: &uploadPB, Text: "Upload", Enabled: m.ticked() > 0, OnClicked: func() { dlg.Accept() }},
					PushButton{AssignTo: &cancelPB, Text: "Cancel", OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
	}.Run(mw)
	if err != nil {
		logf("Upload confirmation failed: %v", err)
		return nil, nil, false
	}
	if cmd != walk.DlgCmdOK {
		return nil, nil, false
	}
	upload, nowSkipped = tickedRows(m.rows, m.ticks)
	return upload, nowSkipped, len(upload) > 0
}

// showSettings opens a dialog to set the MCS API key and pick the track
// results are uploaded to. The track and club lists come from the public MCS
// API. The club only matters for admin keys; race-director keys always upload
// to their own club.
func showSettings() {
	api := cfg.api()
	listed, tracksErr := api.tracks()
	trackHint := "Track:"
	if tracksErr != nil {
		trackHint = "Track (list unavailable):"
		logf("MCS: couldn't get the list of tracks: %v", tracksErr)
	} else {
		gotTracks(listed) // the window's list too, whether or not this dialog is OK'd
	}
	tracks, current := trackChoices(listedTracks, cfg.TrackID, cfg.TrackName)
	names := make([]string, len(tracks))
	for i, t := range tracks {
		names[i] = t.Name
	}
	keyLink := fmt.Sprintf(`<a href="%s">Get your API key from the MCS site</a>`, api.apiKeyPage())

	clubs, _ := api.clubs()
	clubNames := []string{"(key's own club)"}
	currentClub := 0
	for i, c := range clubs {
		clubNames = append(clubNames, c.Name)
		if c.ID == cfg.ClubID {
			currentClub = i + 1
		}
	}

	var dlg *walk.Dialog
	var keyLE *walk.LineEdit
	var trackCB, clubCB *walk.ComboBox
	var limitNE *walk.NumberEdit
	var okPB, cancelPB *walk.PushButton

	newLimit := cfg.FlagLapUnder
	newKey := cfg.APIKey
	newTrackIdx := -1
	newClubIdx := currentClub

	cmd, err := Dialog{
		AssignTo:      &dlg,
		Title:         "MCS Settings",
		DefaultButton: &okPB,
		CancelButton:  &cancelPB,
		MinSize:       Size{Width: 480, Height: 250},
		Layout:        Grid{Columns: 2},
		Children: []Widget{
			Label{Text: "API key:"},
			LineEdit{AssignTo: &keyLE, Text: cfg.APIKey, PasswordMode: true},
			Label{},
			// A link label sizes itself for its maximum width, so it needs one
			// wide enough for the text or it takes the height of a wrapped one.
			LinkLabel{Text: keyLink, MaxSize: Size{Width: 320}, OnLinkActivated: func(link *walk.LinkLabelLink) { openInBrowser(link.URL()) }},
			Label{Text: trackHint},
			ComboBox{AssignTo: &trackCB, Model: names, CurrentIndex: current},
			Label{Text: "Club (admin keys only):"},
			ComboBox{AssignTo: &clubCB, Model: clubNames, CurrentIndex: currentClub},
			Label{Text: "Flag laps quicker than (0 = off):"},
			NumberEdit{AssignTo: &limitNE, Value: cfg.FlagLapUnder, Decimals: 1, MinValue: 0, MaxValue: 600, Suffix: " seconds"},
			Composite{
				ColumnSpan: 2,
				Layout:     HBox{},
				Children: []Widget{
					HSpacer{},
					PushButton{AssignTo: &okPB, Text: "OK", OnClicked: func() {
						newKey = strings.TrimSpace(keyLE.Text())
						newTrackIdx = trackCB.CurrentIndex()
						newClubIdx = clubCB.CurrentIndex()
						newLimit = limitNE.Value()
						dlg.Accept()
					}},
					PushButton{AssignTo: &cancelPB, Text: "Cancel", OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
	}.Run(mw)
	if err != nil {
		logf("MCS settings dialog failed: %v", err)
		return
	}
	if cmd != walk.DlgCmdOK {
		return
	}

	oldTrack := cfg.TrackID
	cfg.FlagLapUnder = newLimit
	if newKey != cfg.APIKey {
		cfg.APIKey = newKey
		meKnown, myClubID, myClubName = false, 0, ""
	}
	if newTrackIdx >= 0 && newTrackIdx < len(tracks) {
		cfg.TrackID = tracks[newTrackIdx].ID
		cfg.TrackName = tracks[newTrackIdx].Name
	}
	if newClubIdx == 0 {
		cfg.ClubID, cfg.ClubName = 0, ""
	} else if newClubIdx > 0 && newClubIdx-1 < len(clubs) {
		cfg.ClubID, cfg.ClubName = clubs[newClubIdx-1].ID, clubs[newClubIdx-1].Name
	}
	saveCfg()
	logf("MCS: settings saved (track: %s)", cfg.TrackName)

	if cfg.TrackID != oldTrack {
		baseline, baselineOK = nil, false
	}
	showTracks()
	if cfg.ShowBoard && cfg.TrackID != 0 && (!baselineOK || !meKnown) {
		fetchBaseline()
	}
	rebuild()
}

func main() {
	cfgFile = "mcs.json"
	if exe, err := os.Executable(); err == nil {
		cfgFile = filepath.Join(filepath.Dir(exe), "mcs.json")
	}
	cfg = loadSettings(cfgFile)

	err := MainWindow{
		AssignTo: &mw,
		Title:    appTitle,
		Size:     Size{Width: 1170, Height: 800},
		MenuItems: []MenuItem{
			Menu{
				Text: "&MCS",
				Items: []MenuItem{
					Action{Text: "&Upload to MCS", OnTriggered: uploadTriggered},
					Action{Text: "Export &video...", OnTriggered: exportVideoTriggered},
					Separator{},
					Action{AssignTo: &showBoardAc, Text: "Show track &leaderboard", Checkable: true, Checked: cfg.ShowBoard, OnTriggered: toggleShowBoard},
					Action{AssignTo: &clubOnlyAc, Text: "My &club only", Checkable: true, Checked: cfg.ClubOnly, OnTriggered: toggleClubOnly},
					Action{Text: "&Refresh MCS times", OnTriggered: fetchBaseline},
					Action{Text: "Reload &track list", OnTriggered: loadTracks},
					Separator{},
					Action{Text: "&Settings...", OnTriggered: showSettings},
				},
			},
			Menu{
				Text: "&Events",
				Items: []MenuItem{
					Action{Text: "&Reload event list", OnTriggered: findEvents},
					Action{Text: "&Choose events folder...", OnTriggered: chooseEventsFolder},
					Action{Text: "Use &FPVTrackside's events folder", OnTriggered: useTracksideFolder},
				},
			},
		},
		Layout: VBox{},
		Children: []Widget{
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					ComboBox{AssignTo: &eventCB, Model: []string{}, StretchFactor: 3},
					PushButton{Text: "Load Event", MaxSize: Size{Width: 110}, OnClicked: loadEvent},
					PushButton{Text: "Upload to MCS", MaxSize: Size{Width: 130}, OnClicked: uploadTriggered},
					PushButton{AssignTo: &exportPB, Text: exportButtonText, MaxSize: Size{Width: 120}, OnClicked: exportVideoTriggered},
					Label{Text: "Track:"},
					ComboBox{AssignTo: &trackCB, Model: []string{}, StretchFactor: 2, OnCurrentIndexChanged: trackChosen},
				},
			},
			Label{AssignTo: &statusLbl},
			TableView{
				AssignTo:         &tableTV,
				StretchFactor:    10,
				AlternatingRowBG: true,
				ColumnsOrderable: false,
				Model:            model,
				StyleCell:        styleCell,
				Columns: []TableViewColumn{
					{Title: "Pos", Width: 45, Alignment: AlignFar},
					{Title: "Move", Width: 60},
					{Title: "Pilot", Width: 180},
					{Title: "Club", Width: 90},
					{Title: "3 laps", Width: 75, Alignment: AlignFar},
					{Title: "Lap 1", Width: 65, Alignment: AlignFar},
					{Title: "Lap 2", Width: 65, Alignment: AlignFar},
					{Title: "Lap 3", Width: 65, Alignment: AlignFar},
					{Title: "Best lap", Width: 70, Alignment: AlignFar},
					{Title: "Rounds", Width: 55, Alignment: AlignFar},
					{Title: "Laps", Width: 45, Alignment: AlignFar},
					{Title: "Check", Width: 300},
				},
			},
			TextEdit{AssignTo: &logTE, ReadOnly: true, VScroll: true, MinSize: Size{Height: 110}, MaxSize: Size{Height: 110}},
		},
	}.Create()
	if err != nil {
		walk.MsgBox(nil, appTitle, fmt.Sprintf("Couldn't open the window: %v", err), walk.MsgBoxIconError)
		os.Exit(1)
	}

	mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		os.Exit(0)
	})

	rebuild()
	showTracks() // the chosen track, until the list arrives
	findEvents()
	if cfg.APIKey == "" {
		askForAPIKey() // its settings page fetches the track list
	} else {
		loadTracks()
	}
	mw.Run()
}
