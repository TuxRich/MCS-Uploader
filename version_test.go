package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The VERSION file has to hold a plain version number: the window's title,
// the installer's name and what the site is told are all made from it.
func TestVersion(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+(\.\d+)?$`).MatchString(version) {
		t.Fatalf("VERSION holds %q, which isn't a version number like 1.1", version)
	}
	if got := versionTitle(); got != "MCS Uploader v"+version {
		t.Errorf("title: %q", got)
	}
}

// Every call to the site says which version is making it.
func TestUserAgent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
		w.Write([]byte(`{"tracks":[]}`))
	}))
	defer srv.Close()
	if _, err := (settings{BaseURL: srv.URL}).api().tracks(); err != nil {
		t.Fatal(err)
	}
	if got != "MCS-Uploader/"+version {
		t.Errorf("the site was told %q", got)
	}
}

// Settings stay beside the program where they already are; otherwise they go
// in the user's own settings folder, which is made when they are first saved.
func TestSettingsPath(t *testing.T) {
	exeDir, userDir := t.TempDir(), t.TempDir()
	inUserDir := filepath.Join(userDir, "MCS Uploader", "mcs.json")
	if got := settingsPath(exeDir, userDir); got != inUserDir {
		t.Errorf("with none beside the program: %s", got)
	}
	// A folder of that name isn't a settings file.
	os.Mkdir(filepath.Join(exeDir, "mcs.json"), 0700)
	if got := settingsPath(exeDir, userDir); got != inUserDir {
		t.Errorf("with a folder called mcs.json beside the program: %s", got)
	}
	os.Remove(filepath.Join(exeDir, "mcs.json"))

	s := loadSettings(inUserDir)
	s.TrackID = 7
	if err := s.save(inUserDir); err != nil {
		t.Fatalf("saving into a folder that isn't there yet: %v", err)
	}
	if got := loadSettings(inUserDir); got.TrackID != 7 {
		t.Errorf("reloaded: %+v", got)
	}

	beside := filepath.Join(exeDir, "mcs.json")
	os.WriteFile(beside, []byte(`{}`), 0600)
	if got := settingsPath(exeDir, userDir); got != beside {
		t.Errorf("with one beside the program: %s", got)
	}
	os.Remove(beside)
	if got := settingsPath(exeDir, ""); got != beside {
		t.Errorf("with no settings folder to use: %s", got)
	}
}

// ffmpeg is looked for where winget puts it, as well as on the PATH.
func TestFFmpegFolders(t *testing.T) {
	local := t.TempDir()
	if got := ffmpegIn(wingetFolders(local)); got != "" {
		t.Errorf("found %s where there is none", got)
	}
	bin := filepath.Join(local, "Microsoft", "WinGet", "Packages", "Gyan.FFmpeg_Microsoft.Winget.Source_8wekyb3d8bbwe", "ffmpeg-8.0-full_build", "bin")
	os.MkdirAll(bin, 0700)
	os.WriteFile(filepath.Join(bin, "ffmpeg.exe"), []byte("x"), 0600)
	if got := ffmpegIn(wingetFolders(local)); got != filepath.Join(bin, "ffmpeg.exe") {
		t.Errorf("in winget's package folder: %q", got)
	}
	links := filepath.Join(local, "Microsoft", "WinGet", "Links")
	os.MkdirAll(links, 0700)
	os.WriteFile(filepath.Join(links, "ffmpeg.exe"), []byte("x"), 0600)
	if got := ffmpegIn(wingetFolders(local)); got != filepath.Join(links, "ffmpeg.exe") {
		t.Errorf("winget's link should come first: %q", got)
	}
	if wingetFolders("") != nil {
		t.Error("folders offered without a %LOCALAPPDATA%")
	}
	// A folder called ffmpeg.exe isn't the program, and gaps in a PATH are skipped.
	odd := t.TempDir()
	os.Mkdir(filepath.Join(odd, "ffmpeg.exe"), 0700)
	if got := ffmpegIn([]string{"", odd}); got != "" {
		t.Errorf("took a folder for the program: %q", got)
	}
	// The PATH in the registry can be read (whatever is on it).
	if len(currentPath()) == 0 {
		t.Error("the registry's PATH came back empty")
	}
}

// What the installer is told is missing.
func TestMissingParts(t *testing.T) {
	trackside := t.TempDir()
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg.exe")
	os.WriteFile(ffmpeg, []byte("x"), 0600)
	nowhere := filepath.Join(t.TempDir(), "none")

	if got := missingParts(settings{FFmpegPath: ffmpeg}, trackside); got != 0 {
		t.Errorf("with everything there: %d", got)
	}
	if got := missingParts(settings{FFmpegPath: filepath.Join(nowhere, "ffmpeg.exe")}, trackside); got != missingFFmpeg {
		t.Errorf("without ffmpeg: %d", got)
	}
	if got := missingParts(settings{FFmpegPath: ffmpeg}, nowhere); got != missingTrackside {
		t.Errorf("without FPVTrackside: %d", got)
	}
	// An events folder chosen by hand stands in for FPVTrackside's own.
	if got := missingParts(settings{FFmpegPath: ffmpeg, EventsFolder: trackside}, nowhere); got != 0 {
		t.Errorf("with a chosen events folder: %d", got)
	}
	if got := missingParts(settings{FFmpegPath: filepath.Join(nowhere, "ffmpeg.exe")}, nowhere); got != missingFFmpeg|missingTrackside {
		t.Errorf("with neither: %d", got)
	}
}

// Help > About says where things are, and never shows the API key.
func TestAboutText(t *testing.T) {
	trackside := t.TempDir()
	text := aboutText(settings{APIKey: "mcs_secret_key", FFmpegPath: filepath.Join(trackside, "none.exe")}, `C:\x\mcs.json`, trackside, "Club", `D:\events`)
	for _, want := range []string{"MCS Uploader v" + version, `C:\x\mcs.json`, "API key:  set", trackside + ` (profile "Club")`, `D:\events`, "ffmpeg:  not found", mcsDefaultBaseURL} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "mcs_secret_key") {
		t.Error("the API key is shown")
	}
	text = aboutText(settings{}, "mcs.json", filepath.Join(trackside, "none"), "", "")
	for _, want := range []string{"API key:  not set", "FPVTrackside:  not found", "Events folder:  not found"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in:\n%s", want, text)
		}
	}
}
