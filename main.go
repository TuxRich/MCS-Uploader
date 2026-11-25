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
	"github.com/lxn/win"
)

const appTitle = "MCS Uploader"

// iconResource is the number the program's icon has among the exe's
// resources: rsrc gives the manifest 1 and the icon the next.
const iconResource = 2

// appIcon is the program's icon, for its windows; nil if it couldn't be
// loaded, when Windows' plain one is shown.
var appIcon walk.Image

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

	meKnown         bool  // MCS has been asked whose the API key is
	me              mcsMe // its answer: a race director's with their club, or an admin's
	listedClubs     []mcsClub
	preparingUpload bool // MCS is being asked about the key or the clubs, before the upload window opens

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
	flagText = walk.RGB(205, 85, 0)    // laps that look wrong
	sameText = walk.RGB(140, 140, 140) // in the upload window: a time MCS has already
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

// clubForBoard is the club the loaded event's pilots fly for on MCS: see
// settings.clubFor.
func clubForBoard() (int64, string) {
	club := cfg.clubFor(loadedID, me, meKnown)
	return club.ID, club.Name
}

// gotMe takes MCS's answer about whose the API key is.
func gotMe(who mcsMe) {
	if !meKnown || who.Role != me.Role || who.ClubID != me.ClubID {
		logf("MCS: API key checked. %s", keyStatus(who, nil))
	}
	meKnown, me = true, who
	// A club chosen for a key that turns out to be a race director's, as the
	// settings used to allow, would only be in the way.
	if !who.admin() && cfg.ClubID != 0 {
		cfg.ClubID, cfg.ClubName = 0, ""
		saveCfg()
	}
}

// checkKey asks MCS whose the API key is, in the background, if that isn't
// known yet.
func checkKey() {
	if meKnown || cfg.APIKey == "" {
		return
	}
	api := cfg.api()
	go func() {
		who, err := api.me()
		mw.Synchronize(func() {
			if api.Key != cfg.APIKey || meKnown {
				return // the key has been changed, or asked about elsewhere, meanwhile
			}
			if err != nil {
				logf("API key: %s", keyStatus(who, err))
				return
			}
			gotMe(who)
			rebuild()
		})
	}()
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
		var who mcsMe
		var whoErr error
		if needMe {
			who, whoErr = api.me()
		}
		board, err := api.leaderboard(trackID)
		mw.Synchronize(func() {
			fetching = false
			if needMe && api.Key == cfg.APIKey && !meKnown {
				if whoErr != nil {
					logf("API key: %s", keyStatus(who, whoErr))
				} else {
					gotMe(who)
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

const uploadTitle = "Upload to MCS"

// canUpload reports whether there is something to upload and somewhere to
// upload it to, saying what is missing if not.
func canUpload() bool {
	switch {
	case cfg.APIKey == "":
		askForAPIKey()
	case cfg.TrackID == 0:
		walk.MsgBox(mw, uploadTitle, "Choose a track first, in the Track list at the top.", walk.MsgBoxIconWarning)
	case loadedID == "":
		walk.MsgBox(mw, uploadTitle, "Load an event first.", walk.MsgBoxIconWarning)
	case len(liveRows) == 0 && len(cfg.uploadIDs(loadedID)) == 0:
		// (An event uploaded before may have times on MCS to take off it.)
		walk.MsgBox(mw, uploadTitle, "No pilot has 3 consecutive laps yet, so there is nothing to upload.", walk.MsgBoxIconInformation)
	default:
		return true
	}
	return false
}

// uploadTriggered is Upload to MCS being pressed. Before the upload window
// opens, MCS is asked in the background for whatever isn't known yet: whose
// the API key is; for an admin's key, the clubs there are to choose from; and,
// for an event that has been uploaded before, what MCS has of it, as uploading
// it again goes by that (see upload.go).
func uploadTriggered() {
	if !canUpload() || preparingUpload {
		return
	}
	api, who, known := cfg.api(), me, meKnown
	eventID, ids := loadedID, cfg.uploadIDs(loadedID)
	if known && !who.admin() && len(ids) == 0 {
		confirmAndUpload(onMCS{})
		return
	}
	preparingUpload = true
	if !known {
		logf("MCS: checking the API key...")
	}
	go func() {
		var err, clubsErr, onErr error
		if !known {
			who, err = api.me()
		}
		var clubs []mcsClub
		var on onMCS
		if err == nil {
			if who.admin() {
				clubs, clubsErr = api.clubs()
			}
			on, onErr = api.eventOnMCS(ids)
		}
		mw.Synchronize(func() {
			preparingUpload = false
			// Not for a key changed or another event loaded meanwhile, or on
			// top of another of the program's windows (the settings, say).
			if api.Key != cfg.APIKey || loadedID != eventID || !win.IsWindowEnabled(mw.Handle()) {
				return
			}
			if err != nil {
				logf("API key: %s", keyStatus(who, err))
				walk.MsgBox(mw, uploadTitle, "Nothing has been uploaded: the API key has to be checked with MCS first.\n\n"+
					keyStatus(who, err)+"\n\nThe key is in MCS > Settings...", walk.MsgBoxIconWarning)
				return
			}
			if !meKnown {
				gotMe(who)
			}
			if clubsErr != nil {
				logf("MCS: couldn't get the list of clubs: %v", clubsErr)
			} else if who.admin() {
				listedClubs = clubs
			}
			if onErr != nil {
				logf("MCS: couldn't read this event's times on MCS: %v", onErr)
				walk.MsgBox(mw, uploadTitle, "Nothing has been uploaded: this event has been uploaded before, and uploading it again goes by "+
					"the times MCS has of it, which couldn't be read.\n\n"+onErr.Error(), walk.MsgBoxIconWarning)
				return
			}
			confirmAndUpload(on)
		})
	}()
}

// clubNamed is a club with its name, as far as that is known here: from the
// club the event is recorded as uploaded for, the key's own, or MCS's list.
func clubNamed(id int64, eventID string) mcsClub {
	club := mcsClub{ID: id}
	switch {
	case id == 0:
		return club
	case cfg.UploadClubs[eventID].ID == id:
		club.Name = cfg.UploadClubs[eventID].Name
	case me.ClubID == id:
		club.Name = me.Club
	default:
		for _, c := range listedClubs {
			if c.ID == id {
				club.Name = c.Name
			}
		}
	}
	if club.Name == "" {
		club.Name = fmt.Sprintf("club %d", id)
	}
	return club
}

// confirmAndUpload shows the upload window and, if it is confirmed, uploads.
// By now MCS has said whose the API key is, and what it has of the event (on)
// if the event has been uploaded before.
func confirmAndUpload(on onMCS) {
	if !canUpload() {
		return
	}
	api, trackID, trackName := cfg.api(), cfg.TrackID, cfg.TrackName
	eventID := loadedID
	uploaded := len(on.IDs) > 0
	if len(liveRows) == 0 && (len(on.Results) == 0 || on.elsewhere(trackID, 0)) {
		walk.MsgBox(mw, uploadTitle, "No pilot has 3 consecutive laps now, so there is nothing to upload.", walk.MsgBoxIconInformation)
		return
	}
	// The club MCS has the event's times for.
	var was mcsClub
	if uploaded {
		was = clubNamed(on.ClubID, eventID)
	}

	// An admin's key says which club the times are for. The window opens with
	// the club the event's times are on MCS for, or else the event's own (see
	// settings.clubFor).
	var clubs []mcsClub
	current := -1
	if me.admin() {
		start := cfg.clubFor(eventID, me, true)
		if was.ID != 0 {
			start = was
		}
		clubs, current = clubChoices(listedClubs, start)
		if len(clubs) == 0 {
			walk.MsgBox(mw, uploadTitle, "Nothing has been uploaded: an admin's key has to say which club the times are for, "+
				"and the list of clubs couldn't be downloaded from MCS. The log has the reason.", walk.MsgBoxIconWarning)
			return
		}
	}

	// What is confirmed is what is sent, even if times change meanwhile.
	lines, ticks, club, moving, ok := confirmUpload(liveRows, on, trackID, trackName, cfg.Skipped[eventID], clubs, current, was)
	if !ok {
		return
	}
	plan := planUpload(lines, ticks)
	skipped := heldBack(lines, ticks)
	if len(skipped) == 0 {
		delete(cfg.Skipped, eventID)
	} else {
		cfg.Skipped[eventID] = skipped
	}
	saveCfg()
	where := fmt.Sprintf("%q", trackName)
	if club.ID != 0 {
		where += " for " + club.Name
	}

	// done is the upload over, well or badly.
	done := func(sent uploadSent, err error) {
		if err != nil {
			logf("MCS: upload failed: %v", err)
			if !moving && sent != (uploadSent{}) {
				logf("MCS: before that, %s. Upload again to finish: what is done already won't be sent twice.", sent)
			}
			return
		}
		// Which uploads on MCS are the event's now.
		var ids []int64
		if !moving {
			ids = append(ids, on.IDs...)
		}
		if sent.NewUpload != 0 {
			ids = append(ids, sent.NewUpload)
		}
		cfg.setUploadIDs(eventID, ids)
		if club.ID != 0 {
			cfg.UploadClubs[eventID] = club
		} else {
			delete(cfg.UploadClubs, eventID)
		}
		saveCfg()
		switch {
		case moving || !uploaded:
			logf("MCS: uploaded %d pilots to track %s (upload %d)", sent.Added, where, sent.NewUpload)
		case !plan.empty():
			logf("MCS: track %s is up to date: %s", where, sent)
		}
		rebuild() // the event's club may have changed, and the board goes by it
		// The videos next, if the laps were recorded: with the times on MCS
		// they can go on YouTube and against those times.
		if loadedID == eventID {
			exportAfterUpload()
		}
	}

	left := ""
	if len(skipped) > 0 {
		left = fmt.Sprintf(", %d left out", len(skipped))
	}
	switch {
	case moving:
		logf("MCS: moving this event's times: uploading %d pilots to %s%s...", len(plan.Add), where, left)
	case !uploaded:
		logf("MCS: uploading %d pilots to %s%s...", len(plan.Add), where, left)
	case plan.empty():
		logf("MCS: nothing to send: the ticked pilots' times are on MCS already, at %s", where)
		done(uploadSent{}, nil)
		return
	default:
		logf("MCS: bringing %s up to date: %s...", where, plan)
	}
	go func() {
		var sent uploadSent
		var err error
		if moving {
			sent.NewUpload, sent.Added, err = api.moveUploads(on.IDs, trackID, club.ID, plan.Add)
		} else {
			sent, err = api.send(trackID, club.ID, plan)
		}
		mw.Synchronize(func() { done(sent, err) })
	}()
}

// The upload window's columns.
const (
	upPilot = iota
	upTime
	upLap1
	upLap2
	upLap3
	upBestLap
	upOnMCS
	upCheck
)

// uploadModel is the list of pilots in the upload confirmation, each with a
// tick box.
type uploadModel struct {
	walk.TableModelBase
	lines   []uploadLine
	ticks   []bool
	checks  []lapCheck // what looks wrong about each line's laps
	changed func()
}

// show sets the lines listed. A pilot listed before keeps their tick; the
// others start ticked unless they are among skipped, the pilots held back
// from the event's upload last time (see heldBack).
func (m *uploadModel) show(lines []uploadLine, skipped []string) {
	before := map[string]bool{}
	for i, l := range m.lines {
		before[nameKey(l.Row.Pilot)] = m.ticks[i]
	}
	ticks := uploadTicks(lines, skipped)
	checks := make([]lapCheck, len(lines))
	for i, l := range lines {
		if tick, listed := before[nameKey(l.Row.Pilot)]; listed {
			ticks[i] = tick
		}
		checks[i] = checkLaps(l.Row.Lap1, l.Row.Lap2, l.Row.Lap3, l.Row.BestLap, cfg.FlagLapUnder)
	}
	m.lines, m.ticks, m.checks = lines, ticks, checks
	m.PublishRowsReset()
}

func (m *uploadModel) RowCount() int { return len(m.lines) }

func (m *uploadModel) Value(row, col int) interface{} {
	if row < 0 || row >= len(m.lines) {
		return ""
	}
	l := m.lines[row]
	switch col {
	case upPilot:
		return l.Row.Pilot
	case upTime:
		return secs(l.Row.Time)
	case upLap1:
		return secs(l.Row.Lap1)
	case upLap2:
		return secs(l.Row.Lap2)
	case upLap3:
		return secs(l.Row.Lap3)
	case upBestLap:
		return secs(l.Row.BestLap)
	case upOnMCS:
		return l.onMCSText()
	case upCheck:
		if l.does() == doesRemove {
			return removalNote(m.ticks[row])
		}
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
	case upLap1, upLap2, upLap3:
		if c.Lap(col - upLap1 + 1) {
			style.TextColor = flagText
		}
	case upBestLap:
		if c.BestUnder {
			style.TextColor = flagText
		}
	case upOnMCS:
		// What ticking the pilot does to MCS's time stands out; a time that
		// is there already, as it is, stands back.
		switch m.lines[row].does() {
		case doesAdd, doesChange:
			style.TextColor = newText
		case doesRemove:
			style.TextColor = flagText
		default:
			style.TextColor = sameText
		}
	case upCheck:
		style.TextColor = flagText
	}
}

func (m *uploadModel) Checked(row int) bool { return m.ticks[row] }

// SetChecked is a tick box being ticked or unticked. The table draws the row
// again afterwards, which is how a note that goes by the tick keeps up.
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

// confirmUpload shows the pilots and times about to be uploaded, each with a
// tick box, and asks whether to go ahead. Pilots in skipped, those held back
// from the event's upload last time (see heldBack), start unticked.
//
// on is what MCS has of the event already, if it has been uploaded before.
// The window then lists, beside each pilot's time, the time MCS has, and
// after the event's pilots those MCS has a time for who have none in the
// event now: see uploadLines. That is unless the upload is a move, to
// another track or club than MCS has the event's times for: those are then
// replaced altogether, and the window lists the event's times alone.
//
// An admin's key also chooses the club the times are for, from clubs, with
// the current one chosen to begin with (-1 for none); was is the club MCS has
// the event's times for. With no clubs there is no choice, and the club
// returned has ID 0: the key's own.
//
// It returns the lines as they were listed, which of them were ticked, the
// club, whether the upload is a move, and whether it was confirmed.
func confirmUpload(rows []mcsRow, on onMCS, trackID int64, trackName string, skipped []string, clubs []mcsClub, current int, was mcsClub) (lines []uploadLine, ticks []bool, club mcsClub, moving, ok bool) {
	uploaded := len(on.IDs) > 0
	otherTrack := on.elsewhere(trackID, 0)
	inPlace, afresh := uploadLines(rows, on.Results), uploadLines(rows, nil)
	// linesFor is what the window lists when the times are for club c.
	linesFor := func(c mcsClub) []uploadLine {
		if on.elsewhere(trackID, c.ID) {
			return afresh
		}
		return inPlace
	}
	choosesClub := len(clubs) > 0
	clubNames := make([]string, len(clubs))
	for i, c := range clubs {
		clubNames[i] = c.Name
	}
	var start mcsClub
	if choosesClub && current >= 0 && current < len(clubs) {
		start = clubs[current]
	}
	m := &uploadModel{}
	m.show(linesFor(start), skipped)

	var dlg *walk.Dialog
	var tv *walk.TableView
	var countLbl, clubHintLbl, noteLbl *walk.Label
	var clubCB *walk.ComboBox
	var uploadPB, cancelPB *walk.PushButton
	// chosen is the club chosen in the window, with ID 0 if none is.
	chosen := func() mcsClub {
		if !choosesClub {
			return mcsClub{}
		}
		if i := clubCB.CurrentIndex(); i >= 0 && i < len(clubs) {
			return clubs[i]
		}
		return mcsClub{}
	}
	clubHint := func(c mcsClub) string {
		if c.ID == 0 {
			return "Choose the club these pilots fly for."
		}
		return ""
	}
	countText := func(c mcsClub) string {
		return uploadSummary(m.lines, m.ticks, m.checks, uploaded && !on.elsewhere(trackID, c.ID))
	}
	m.changed = func() {
		countLbl.SetText(countText(chosen()))
		uploadPB.SetEnabled(m.ticked() > 0 && (!choosesClub || chosen().ID != 0))
	}

	intro := fmt.Sprintf("These are the event's times, for upload to %q. Untick a pilot to leave their time out.", trackName)
	dvr := fmt.Sprintf(`You can add links to DVR afterwards, on the <a href="%s">DVR page of the MCS site</a>.`, cfg.api().dvrPage())

	err := Dialog{
		AssignTo:      &dlg,
		Icon:          appIcon,
		Title:         uploadTitle,
		DefaultButton: &uploadPB,
		CancelButton:  &cancelPB,
		MinSize:       Size{Width: 1020, Height: 560},
		Layout:        VBox{},
		Children: []Widget{
			Label{Text: intro},
			// Only an admin's key has the choice of club.
			Composite{
				Visible: choosesClub,
				Layout:  HBox{MarginsZero: true},
				Children: []Widget{
					Label{Text: "Club the times are for:"},
					ComboBox{AssignTo: &clubCB, Model: clubNames, CurrentIndex: current, MinSize: Size{Width: 260}},
					Label{AssignTo: &clubHintLbl, Text: clubHint(start)},
					HSpacer{},
				},
			},
			TableView{
				AssignTo:         &tv,
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
					// The time MCS has, when the upload goes by that.
					{Title: "On MCS", Width: 115, Alignment: AlignFar, Hidden: !uploaded || on.elsewhere(trackID, start.ID)},
					{Title: "Check", Width: 330},
				},
			},
			Label{AssignTo: &countLbl, Text: countText(start)},
			Label{AssignTo: &noteLbl, Visible: uploaded, Text: uploadNote(otherTrack, was, start)},
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
					PushButton{AssignTo: &uploadPB, Text: "Upload", Enabled: m.ticked() > 0 && (!choosesClub || start.ID != 0), OnClicked: func() {
						club = chosen()
						dlg.Accept()
					}},
					PushButton{AssignTo: &cancelPB, Text: "Cancel", OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
	}.Create(mw)
	if err != nil {
		logf("Upload confirmation failed: %v", err)
		return nil, nil, mcsClub{}, false, false
	}
	// Attached once the window is all there, as the first choice is made
	// while it is being put together.
	clubCB.CurrentIndexChanged().Attach(func() {
		c := chosen()
		clubHintLbl.SetText(clubHint(c))
		noteLbl.SetText(uploadNote(otherTrack, was, c))
		// Another club than MCS has the event's times for makes it a move,
		// and the list a different one.
		m.show(linesFor(c), skipped)
		tv.Columns().At(upOnMCS).SetVisible(uploaded && !on.elsewhere(trackID, c.ID))
		tv.Invalidate()
		m.changed()
	})
	if dlg.Run() != walk.DlgCmdOK {
		return nil, nil, mcsClub{}, false, false
	}
	return m.lines, m.ticks, club, on.elsewhere(trackID, club.ID), m.ticked() > 0 && (!choosesClub || club.ID != 0)
}

// showSettings opens a dialog to set the MCS API key and pick the track
// results are uploaded to. The track and club lists come from the public MCS
// API.
//
// The club is only an admin key's to choose: a race director's key always
// uploads for its own club. So the choice of club is hidden until MCS has
// said the key in the box is an admin's, which it is asked in the background
// whenever the box holds a whole key.
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

	// The clubs are only wanted if the key is an admin's, but the list is
	// short and anyone's to read, so it is there by the time MCS has said.
	if list, err := api.clubs(); err != nil {
		logf("MCS: couldn't get the list of clubs: %v", err)
	} else {
		listedClubs = list
	}
	clubs, currentClub := clubChoices(listedClubs, mcsClub{cfg.ClubID, cfg.ClubName})
	clubNames := make([]string, len(clubs))
	for i, c := range clubs {
		clubNames[i] = c.Name
	}

	var dlg *walk.Dialog
	var keyLE *walk.LineEdit
	var keyLbl, clubLbl *walk.Label
	var trackCB, clubCB *walk.ComboBox
	var limitNE *walk.NumberEdit
	var okPB, cancelPB *walk.PushButton

	newLimit := cfg.FlagLapUnder
	newKey := cfg.APIKey
	newTrackIdx := -1
	newClubIdx := currentClub

	err := Dialog{
		AssignTo:      &dlg,
		Icon:          appIcon,
		Title:         "MCS Settings",
		DefaultButton: &okPB,
		CancelButton:  &cancelPB,
		MinSize:       Size{Width: 480, Height: 250},
		Layout:        Grid{Columns: 2},
		Children: []Widget{
			Label{Text: "API key:"},
			LineEdit{AssignTo: &keyLE, Text: cfg.APIKey, PasswordMode: true},
			Label{},
			// What MCS says about the key in the box.
			Label{AssignTo: &keyLbl},
			Label{},
			// A link label sizes itself for its maximum width, so it needs one
			// wide enough for the text or it takes the height of a wrapped one.
			LinkLabel{Text: keyLink, MaxSize: Size{Width: 320}, OnLinkActivated: func(link *walk.LinkLabelLink) { openInBrowser(link.URL()) }},
			Label{Text: trackHint},
			ComboBox{AssignTo: &trackCB, Model: names, CurrentIndex: current},
			Label{AssignTo: &clubLbl, Text: "Club to upload for:", Visible: false},
			ComboBox{AssignTo: &clubCB, Model: clubNames, CurrentIndex: currentClub, Visible: false},
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
	}.Create(mw)
	if err != nil {
		logf("MCS settings dialog failed: %v", err)
		return
	}

	// asked is the key MCS was last asked about, and answer what it said, nil
	// while that isn't known: still being asked, or the key isn't one of its.
	var asked string
	var answer *mcsMe
	closed := false
	show := func(status string, admin bool) {
		keyLbl.SetText(status)
		clubLbl.SetVisible(admin)
		clubCB.SetVisible(admin)
	}
	key := func() string { return strings.TrimSpace(keyLE.Text()) }
	ask := func() {
		k := key()
		if closed || k == asked {
			return
		}
		asked, answer = k, nil
		if k == "" {
			show("", false)
			return
		}
		show("Checking the key with MCS...", false)
		go func() {
			who, err := mcsAPI{Base: api.Base, Key: k}.me()
			mw.Synchronize(func() {
				if closed || asked != k {
					return // the window has gone, or the box holds something else now
				}
				if err == nil {
					answer = &who
				}
				show(keyStatus(who, err), err == nil && who.admin())
			})
		}()
	}
	switch {
	case cfg.APIKey == "":
	case meKnown:
		// Already asked about, since the program started.
		who := me
		asked, answer = cfg.APIKey, &who
		show(keyStatus(who, nil), who.admin())
	default:
		ask()
	}
	// A whole key in the box, as after a paste, is asked about at once.
	// Anything else is asked about when the box is left, in case keys are one
	// day a different length.
	keyLE.TextChanged().Attach(func() {
		switch k := key(); {
		case k == asked:
		case keyShaped(k):
			ask()
		default:
			// What MCS said was about a different key.
			asked, answer = "", nil
			show("", false)
		}
	})
	keyLE.EditingFinished().Attach(ask)
	dlg.Closing().Attach(func(*bool, walk.CloseReason) { closed = true })

	cmd := dlg.Run()
	closed = true
	if cmd != walk.DlgCmdOK {
		return
	}

	oldTrack := cfg.TrackID
	cfg.FlagLapUnder = newLimit
	if newKey != cfg.APIKey {
		cfg.APIKey = newKey
		meKnown, me = false, mcsMe{}
	}
	if newTrackIdx >= 0 && newTrackIdx < len(tracks) {
		cfg.TrackID = tracks[newTrackIdx].ID
		cfg.TrackName = tracks[newTrackIdx].Name
	}
	// The club was there to choose if MCS said the key is an admin's. For a
	// race director's key there is none to choose (gotMe sees to that), and
	// if MCS hasn't said, the choice is left as it was.
	if answer != nil && asked == newKey {
		gotMe(*answer)
		if answer.admin() {
			cfg.ClubID, cfg.ClubName = 0, ""
			if newClubIdx >= 0 && newClubIdx < len(clubs) {
				cfg.ClubID, cfg.ClubName = clubs[newClubIdx].ID, clubs[newClubIdx].Name
			}
		}
	}
	saveCfg()
	logf("MCS: settings saved (track: %s)", cfg.TrackName)

	if cfg.TrackID != oldTrack {
		baseline, baselineOK = nil, false
	}
	showTracks()
	if cfg.ShowBoard && cfg.TrackID != 0 && (!baselineOK || !meKnown) {
		fetchBaseline()
	} else {
		checkKey()
	}
	rebuild()
}

// showAbout is Help > About: the version, and where the program is finding
// things.
func showAbout() {
	walk.MsgBox(mw, "About "+appTitle, aboutText(cfg, cfgFile, tracksideDir, tracksideProfile, eventsDir), walk.MsgBoxIconInformation)
}

func main() {
	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	userDir, _ := os.UserConfigDir()
	cfgFile = settingsPath(exeDir, userDir)
	cfg = loadSettings(cfgFile)
	tracksideDir = filepath.Join(os.Getenv("LOCALAPPDATA"), "FPVTrackside")

	// The installer, asking what this computer hasn't got: no window.
	if len(os.Args) > 1 && os.Args[1] == "-check" {
		os.Exit(missingParts(cfg, tracksideDir))
	}
	markRunning()
	// The program's icon is in the exe (rsrc.syso, made from icon.ico).
	if icon, err := walk.NewIconFromResourceId(iconResource); err == nil {
		appIcon = icon
	}

	err := MainWindow{
		AssignTo: &mw,
		Title:    versionTitle(),
		Icon:     appIcon,
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
			Menu{
				Text: "&Help",
				Items: []MenuItem{
					Action{Text: "&About " + appTitle, OnTriggered: showAbout},
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

	logf("%s. Settings: %s", versionTitle(), cfgFile)
	rebuild()
	showTracks() // the chosen track, until the list arrives
	findEvents()
	if cfg.APIKey == "" {
		askForAPIKey() // its settings page fetches the track list
	} else {
		loadTracks()
		checkKey() // whose it is: an admin's chooses the club it uploads for
	}
	mw.Run()
}
