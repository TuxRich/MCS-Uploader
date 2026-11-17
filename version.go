package main

import (
	_ "embed"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// The version number is kept in the VERSION file, which the installer reads
// as well, so the program and its installer can't disagree about it.
//
//go:embed VERSION
var versionFile string

// version is the program's version, like "1.1".
var version = strings.TrimSpace(versionFile)

// versionTitle is the program's name with its version, as in the window's
// title, so that someone with a problem can say which version they have.
func versionTitle() string { return appTitle + " v" + version }

// userAgent is how the program names itself to the MCS site, so the site's
// own records show which version sent what.
func userAgent() string { return "MCS-Uploader/" + version }

// runningMutex is the installer's AppMutex: while the program holds it, the
// installer knows the program is running and asks for it to be closed before
// replacing or removing it.
const runningMutex = "MCS-Uploader-Running"

// markRunning takes the mutex. It is let go when the program ends.
func markRunning() {
	if name, err := windows.UTF16PtrFromString(runningMutex); err == nil {
		windows.CreateMutex(nil, false, name)
	}
}

// What "MCS-Uploader.exe -check" exits with: the sum of whichever of these
// apply, so 0 when nothing is missing. The installer runs it to see what the
// program needs that this computer hasn't got, using the program's own way
// of looking.
const (
	missingFFmpeg    = 1 // Export Video can't cut videos
	missingTrackside = 2 // there are no events to read
)

// missingParts is that sum.
func missingParts(cfg settings, tracksideDir string) int {
	code := 0
	if _, err := findFFmpeg(cfg.FFmpegPath, tracksideDir); err != nil {
		code |= missingFFmpeg
	}
	if !isFolder(tracksideDir) && !isFolder(cfg.EventsFolder) {
		code |= missingTrackside
	}
	return code
}

func isFolder(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// aboutText is what Help > About shows: the version, and where the program
// is finding things, which is what is asked first when something isn't
// working. It never includes the API key.
func aboutText(cfg settings, cfgFile, tracksideDir, profile, eventsDir string) string {
	trackside := "not found on this computer"
	if isFolder(tracksideDir) {
		trackside = tracksideDir
		if profile != "" {
			trackside += fmt.Sprintf(" (profile %q)", profile)
		}
	}
	if eventsDir == "" {
		eventsDir = "not found"
	}
	ffmpeg, err := findFFmpeg(cfg.FFmpegPath, tracksideDir)
	if err != nil {
		ffmpeg = "not found (Export Video needs it)"
	}
	key := "set"
	if cfg.APIKey == "" {
		key = "not set"
	}
	return strings.Join([]string{
		versionTitle(),
		"",
		"Settings:  " + cfgFile,
		"API key:  " + key,
		"MCS site:  " + cfg.api().Base,
		"FPVTrackside:  " + trackside,
		"Events folder:  " + eventsDir,
		"ffmpeg:  " + ffmpeg,
		"",
		"Ctrl+C copies this, to send with a question.",
	}, "\n")
}
