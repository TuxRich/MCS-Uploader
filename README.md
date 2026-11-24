# MCS Uploader

Follows an FPVTrackside event live and uploads its best 3-lap times to the
MCS (Micro Club Sprint) site. It is the MCS part of Trackside-to-SQLite on its
own: no database, lineups or lap graphs.

## Installing

Run `MCS-Uploader-Setup-v<version>.exe`. It installs for you only, so it needs
no administrator, and puts MCS Uploader in the Start menu and, unless you
untick it, on the desktop.

Before it installs, it looks for the two programs MCS Uploader works with and
shows what it found:

- **FPVTrackside**, whose events MCS Uploader reads. If it isn't there the
  installer says so and carries on.
- **ffmpeg**, which Export Video cuts the videos with. If it isn't there the
  installer offers to download and install it (about 110 MB) with winget,
  Windows' own installer of programs. Untick that to do without; everything
  but Export Video works without ffmpeg.

Things to know:

- The installer isn't signed, so Windows may say it "protected your PC" when
  it is run from a download. **More info**, then **Run anyway**.
- Installing a newer version over an older one keeps your settings. Close MCS
  Uploader first; the installer asks if it is still open.
- To remove it, use Windows' **Installed apps**. You are asked whether to
  remove your settings (API key, track, which events were uploaded) as well.

### Which version you have

The version is in the window's title (`MCS Uploader v1.2`) and is the first
line of the log. **Help > About MCS Uploader** shows it with where the program
is finding its settings, FPVTrackside, the events folder and ffmpeg: what to
send with a question. Ctrl+C copies it. It never shows the API key.

The MCS site is also told the version with everything the program sends it
(`MCS-Uploader/1.2`), so the site's records show which version uploaded what.

## Building

```
build.cmd
MCS-Uploader.exe
```

Windows only. `go test ./...` runs the tests.

`release.cmd` tests and builds the program and then its installer, into
`dist\MCS-Uploader-Setup-v<version>.exe`. It needs
[Inno Setup 6](https://jrsoftware.org/isinfo.php)
(`winget install JRSoftware.InnoSetup`); the installer itself is
`installer\MCS-Uploader.iss`.

- **The version number** is the one line in the `VERSION` file. The program,
  the installer's name and Windows' list of installed apps all take it from
  there, so that is the only place to change it.
- **The icon**, `icon.ico`, is made from `logo.png` by
  `go run ./installer/mkicon`, which also makes the picture on the
  installer's pages. The exe carries the icon in `rsrc.syso`; after changing
  the icon, make that again with [rsrc](https://github.com/akavel/rsrc):
  `rsrc -manifest MCS-Uploader.exe.manifest -ico icon.ico -o rsrc.syso`.

## Using it

1. The first time, the program says the API key hasn't been set up and opens
   the settings (also at **MCS > Settings...**). Paste your API key; the link
   under it opens the page on the MCS site where you get one.
   - As soon as the box holds a whole key, pasted or typed, MCS is asked whose
     it is and the answer is shown under the box: a race director's, and for
     which club; an admin's; or a key MCS doesn't know.
   - A race director's key always uploads for its own club, so there is no
     club to choose. For an admin's key a **Club to upload for** list appears:
     the club the upload window starts at.
2. Choose the track in the **Track** list at the top of the window.
3. Pick the event and press **Load Event**. The table shows each pilot's best
   3 consecutive laps from any round of the event and updates every 3 seconds
   while Trackside runs the event.
4. **Upload to MCS** lists the pilots and times about to be sent, each with a
   tick box. Untick a pilot to leave their time out, then press **Upload**.
   Nothing is sent until then, and nothing is uploaded automatically.
   - Uploading the same event again replaces its previous upload rather than
     adding a second set, so a pilot unticked the second time is removed.
   - The pilots you untick are remembered for that event and start unticked
     next time.
   - With an admin's key the window also has a **Club the times are for**
     list. It starts at the club in the settings; choosing another is for
     this upload only and leaves the settings alone. An event uploaded again
     starts at the club it was uploaded for last time. An event has one
     upload, so uploading it again for another club moves its times to that
     club; the window says so before you press **Upload**.
   - DVR links are added afterwards on the MCS site; the window has a link to
     that page.
   - Once the times are uploaded, the **Export Video** window (below) opens
     by itself if FPVTrackside recorded any of these pilots' laps, so the
     videos can follow the times. Press **Cancel** in it if you don't want
     them. It doesn't open if there is no recording to export.

### Export video

**Export Video** cuts a video of each pilot's 3 laps, with 3 seconds before
and after, out of the recordings FPVTrackside makes of its races. Add them to
your times afterwards on the MCS site's DVR page.

1. A window lists the pilots with tick boxes. A pilot whose laps have no
   usable recording is greyed out, with the reason, and can't be ticked.
2. Press **Export...** and choose the folder to save into. It opens at the
   folder used last time.
3. The clips are cut one after another; the log at the bottom shows progress,
   and the button becomes **Stop Export** while it runs.

Files are named `MCS - <track> - <pilot> - <time>.mp4`.

- By default a clip is just the pilot's own picture, cut out of the recording
  (which has all the pilots' pictures side by side). Untick "Only the pilot's
  own picture" to keep the whole recorded picture.
- By default a timer is drawn at the top right of the video. It shows 0 until
  the first lap starts, counts to the millisecond through the three laps, and
  stops on the pilot's 3-lap time: `24.617`, or `1:24.617` for a minute or
  more. Under it, a little smaller, each lap's time is added as the lap is
  finished (`L1 13.613`) and stays to the end. Untick "Timer on the video" to
  leave both off.
- By default the MCS logo is put in the bottom right corner as a watermark,
  a quarter of the picture's width and slightly see-through. Untick "MCS logo
  in the corner of the video" to leave it off. The logo is built into the
  program; to use another, put a `logo.png` with a see-through background
  beside `MCS-Uploader.exe`.
- All three choices are remembered.
- A video less than 720 pixels high is enlarged to 720 high: one pilot's
  picture out of four is only about 476 by 356, and YouTube and the like
  give a video that small so little data that it looks bad. Enlarging adds no
  detail, but gets the video treated as HD. The files are larger and take
  longer to make. To enlarge to a different height put, say,
  `"video_min_height": 1080` in `mcs.json`; `0` leaves videos the size they
  are.
- FPVTrackside only records a video source that has **Record Video For
  Replays** turned on in its video settings, and it only keeps so many videos
  (**Videos To Keep**), deleting the oldest. If nothing can be exported the
  program says which of those is the reason.
- The race a pilot's laps are in has to have finished: Trackside can't share a
  recording it is still writing.
- The cutting is done by [ffmpeg](https://ffmpeg.org), which has to be
  installed. The installer offers to do that; otherwise
  `winget install ffmpeg`. If it isn't on the PATH, put the full path of
  `ffmpeg.exe` in `mcs.json` as `"ffmpeg_path"`.

#### Straight onto YouTube

If the MCS site has the league's YouTube channel connected, the export window
offers **Put each video on the MCS YouTube channel, and its link against the
pilot's time on MCS**. Each clip is then uploaded as soon as it is cut, and
its link is put on the pilot's time for you: there is nothing to paste on the
DVR page. No YouTube account is needed, only your MCS API key; the videos go
on the league's channel, named `MCS - <track> - <pilot> - <time>`.

The option is ticked to begin with. Untick it to keep the videos off YouTube;
like the other choices, that is remembered.

- **Upload the event's times first.** A video goes against a time that is
  already on MCS. The **On MCS** column says, for each pilot, whether their
  video can go up, or why not: no time on MCS, a time that has changed since
  it was uploaded (upload the times again), or a video already there.
- A pilot who already has a video on MCS is left alone. To replace it, clear
  the link on the MCS DVR page first.
- When the option can't be used the window says why: no API key, the site
  has no channel connected, or the event's times haven't been uploaded.
- Each club can upload 40 videos a day.
- **Stop Export** stops uploads as well. A video being sent when it is
  pressed is abandoned.

### Track leaderboard

**MCS > Show track leaderboard** downloads the track's times from MCS and shows
the event's times in among them:

- pilots flying in the event are highlighted;
- **Move** shows `▲ 3` for places gained against the MCS board, `▼ 1` for
  places lost, and `NEW` for a pilot who wasn't on it;
- a time in green is one set in this event that beats the pilot's MCS time.

**My club only** cuts the board down to your club, with places counted within
it. **Refresh MCS times** downloads the board again.

A pilot in the event is matched to an MCS pilot by name (ignoring case) within
your club, the same way MCS matches names on upload. With an admin's key,
"your club" is the club the loaded event was last uploaded for, or else the
one in the settings.

The MCS board is the comparison point, so it is only downloaded when an event
is loaded or you press Refresh. Once you have uploaded an event, refreshing
makes its times part of the board and the markers for them go.

## How times are worked out

The rules are Trackside-to-SQLite's, except that every type of round counts
where it counts time trial rounds only:

- every valid race counts, whatever type of round it is in (time trial, race,
  practice...);
- a lap counts if the detection that ended it is valid;
- the holeshot (lap 0) is never part of a time;
- the 3 laps must be one after another in the same race;
- pilots are told apart by name.

Split-gate sector times are uploaded too when a lap has all of them.

## Laps to check

A pilot is flagged, in the **Check** column of the main table and of the
upload window, with the laps concerned in orange, when:

- one of the 3 laps, or the pilot's best lap, is under 9 seconds. The limit is
  in **MCS > Settings...**, as tracks differ; 0 turns it off;
- one of the 3 laps is a lot quicker than the other two: over a quarter
  quicker than both. A missed gate or a false detection looks like this. One
  slow lap, as after a crash, isn't flagged.

A flag doesn't stop anything: flagged pilots stay ticked in the upload window
and it is up to you to untick them.

## Where things are

- Events are read from FPVTrackside's own events folder, found at start-up
  from its settings (`%LOCALAPPDATA%\FPVTrackside`, the loaded profile's Event
  Storage Location). If it can't be opened you are asked to choose a folder;
  the **Events** menu changes it or goes back to FPVTrackside's.
- Settings are in `mcs.json`, in `%APPDATA%\MCS Uploader`. **Help > About**
  shows exactly where. It holds the API key.
  - If there is a `mcs.json` beside the program, that one is used instead.
    That is where settings were kept before v1.1, so a copy of the program
    with its settings beside it carries on as it was; it is also how a copy
    can be given settings of its own. An installed copy doesn't see those:
    put your API key in again, or copy that `mcs.json` into
    `%APPDATA%\MCS Uploader`.
  - `mcs.json` is git-ignored, for the copy beside a program built here.
  - `"base_url"` in it points the program at another MCS server for testing.
