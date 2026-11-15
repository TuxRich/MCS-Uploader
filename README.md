# MCS Uploader

Follows an FPVTrackside event live and uploads its best 3-lap times to the
MCS (Micro Club Sprint) site. It is the MCS part of Trackside-to-SQLite on its
own: no database, lineups or lap graphs.

## Build and run

```
build.cmd
MCS-Uploader.exe
```

Windows only. `go test ./...` runs the tests.

## Using it

1. The first time, the program says the API key hasn't been set up and opens
   the settings (also at **MCS > Settings...**). Paste your API key; the link
   under it opens the page on the MCS site where you get one. Admin keys also
   choose the club; race director keys always upload to their own club.
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
   - DVR links are added afterwards on the MCS site; the window has a link to
     that page.

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
- FPVTrackside only records a video source that has **Record Video For
  Replays** turned on in its video settings, and it only keeps so many videos
  (**Videos To Keep**), deleting the oldest. If nothing can be exported the
  program says which of those is the reason.
- The race a pilot's laps are in has to have finished: Trackside can't share a
  recording it is still writing.
- The cutting is done by [ffmpeg](https://ffmpeg.org), which has to be
  installed (`winget install ffmpeg`). If it isn't on the PATH, put the full
  path of `ffmpeg.exe` in `mcs.json` as `"ffmpeg_path"`.

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
your club, the same way MCS matches names on upload.

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
- Settings are in `mcs.json` beside the program. It holds the API key, so it
  is git-ignored. `"base_url"` in it points the program at another MCS server
  for testing.
