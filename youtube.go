package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Putting exported videos on the league's YouTube channel, through the MCS
// site. The site holds the channel's sign-in, so nothing is needed here but
// the MCS API key:
//
//  1. the site is asked to start an upload for a pilot's time; it opens one
//     on YouTube, named after the time, and gives back a one-off address;
//  2. the video is sent straight to YouTube at that address;
//  3. the site is told it is done; it checks with YouTube and puts the
//     video's link against the time.
//
// Google doesn't say whether it will take a video at that address from a
// program that isn't signed in to the channel. If it won't, the video is sent
// to the site instead, which passes it on.

// videoClient sends videos. It has no time limit of its own, as a video takes
// as long as the connection needs; the context it is given can stop it.
var videoClient = &http.Client{}

// videoStart is the site's answer to starting a video upload.
type videoStart struct {
	UploadID    string `json:"upload_id"`
	UploadURL   string `json:"upload_url"`
	ContentType string `json:"content_type"`
	Title       string `json:"title"`
}

// videoLinked is the site's answer once a video is against its time.
type videoLinked struct {
	VideoURL string `json:"video_url"`
}

// putVideo sends a video file to an address in one request. key is the MCS
// API key, sent only when the address is the MCS site's.
func putVideo(ctx context.Context, url, file string, size int64, contentType, key string) (status int, reply []byte, err error) {
	f, err := os.Open(file)
	if err != nil {
		return 0, nil, err
	}
	defer f.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, f)
	if err != nil {
		return 0, nil, err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", userAgent())
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	resp, err := videoClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	reply, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, reply, nil
}

// refusal is the reason given in a reply that wasn't a success.
func refusal(status int, reply []byte) string {
	var mcs struct {
		Error string `json:"error"`
	}
	var google struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	why := ""
	switch {
	case json.Unmarshal(reply, &mcs) == nil && mcs.Error != "":
		why = mcs.Error
	case json.Unmarshal(reply, &google) == nil && google.Error.Message != "":
		why = google.Error.Message
	default:
		why = strings.TrimSpace(string(reply))
		if len(why) > 200 {
			why = why[:200]
		}
	}
	if why == "" {
		why = http.StatusText(status)
	}
	return fmt.Sprintf("%s (%d)", why, status)
}

// uploadVideo puts a video file on the league's YouTube channel as the DVR of
// one of the club's times, and returns the video's link once MCS has it
// against the time.
func (a mcsAPI) uploadVideo(ctx context.Context, resultID int64, file string) (link string, err error) {
	st, err := os.Stat(file)
	if err != nil {
		return "", err
	}
	size := st.Size()

	var start videoStart
	err = a.do("POST", fmt.Sprintf("/my/results/%d/video", resultID), true,
		map[string]any{"size": size, "content_type": "video/mp4"}, &start)
	if err != nil {
		return "", err
	}
	if start.UploadID == "" || start.UploadURL == "" {
		return "", errors.New("MCS started the upload but didn't say where to send the video")
	}
	if start.ContentType == "" {
		start.ContentType = "video/mp4"
	}

	// Straight to YouTube. The address is the permission: no key goes with it.
	status, reply, err := putVideo(ctx, start.UploadURL, file, size, start.ContentType, "")
	if err != nil {
		return "", fmt.Errorf("sending the video to YouTube: %w", err)
	}
	var linked videoLinked
	switch {
	case status == http.StatusOK || status == http.StatusCreated:
		// YouTube has it. The site checks that for itself and links it.
		if err := a.do("POST", "/my/video-uploads/"+start.UploadID+"/finish", true, nil, &linked); err != nil {
			return "", err
		}
	case status >= 400 && status < 500 && status != http.StatusNotFound:
		// YouTube won't take it from here: through the site, then, which
		// passes it on with the channel's sign-in and links it in one go.
		status, reply, err = putVideo(ctx, a.Base+"/api/v1/my/video-uploads/"+start.UploadID, file, size, start.ContentType, a.Key)
		if err != nil {
			return "", fmt.Errorf("sending the video through MCS: %w", err)
		}
		if status != http.StatusOK {
			return "", fmt.Errorf("sending the video through MCS: %s", refusal(status, reply))
		}
		if err := json.Unmarshal(reply, &linked); err != nil {
			return "", errors.New("MCS took the video but its answer couldn't be read")
		}
	default:
		return "", fmt.Errorf("YouTube didn't take the video: %s", refusal(status, reply))
	}
	if linked.VideoURL == "" {
		return "", errors.New("MCS took the video but gave no link for it")
	}
	return linked.VideoURL, nil
}

// videoOffer is whether exported videos can be put on the league's YouTube
// channel, and if so which time on MCS each would go against.
type videoOffer struct {
	Available bool
	Why       string       // why not, when they can't be
	Matches   []videoMatch // one for each run, when they can
}

// checkVideoUpload works out whether an event's videos can be uploaded
// through MCS. hasKey is whether an API key is set, and uploadIDs the event's
// uploads of times on MCS, none if it hasn't been uploaded.
func checkVideoUpload(api mcsAPI, hasKey bool, uploadIDs []int64, runs []lapRun) videoOffer {
	if !hasKey {
		return videoOffer{Why: "Set your MCS API key first (MCS > Settings...)."}
	}
	me, err := api.me()
	if err != nil {
		return videoOffer{Why: fmt.Sprintf("MCS couldn't be asked: %v.", err)}
	}
	if !me.VideoUpload {
		return videoOffer{Why: "The MCS site has no YouTube channel connected for this."}
	}
	if len(uploadIDs) == 0 {
		return videoOffer{Why: "Upload this event's times to MCS first, so each video has a time to go against."}
	}
	on, err := api.eventOnMCS(uploadIDs)
	if err != nil {
		return videoOffer{Why: fmt.Sprintf("MCS couldn't be asked: %v.", err)}
	}
	if len(on.IDs) == 0 {
		return videoOffer{Why: "This event's times are no longer on MCS: upload them again first."}
	}
	return videoOffer{Available: true, Matches: matchVideos(runs, on.Results)}
}

// videoMatch says, for one pilot's exported video, which of the club's times
// on MCS it is the DVR of, or why it can't be uploaded.
type videoMatch struct {
	ResultID int64  // the time's ID on MCS; 0 if there is none to link to
	Why      string // why it can't be uploaded and linked; "" if it can
}

// matchVideos pairs each run with the time MCS has for that pilot in the
// event's upload. A video is only offered for upload when MCS has exactly
// that time and no video against it yet: the site names the video after the
// time it has, so the two must agree.
func matchVideos(runs []lapRun, results []mcsResult) []videoMatch {
	byName := map[string]mcsResult{}
	for _, r := range results {
		if _, taken := byName[nameKey(r.Pilot)]; !taken {
			byName[nameKey(r.Pilot)] = r
		}
	}
	matches := make([]videoMatch, len(runs))
	for i, run := range runs {
		r, ok := byName[nameKey(run.Name)]
		switch {
		case !ok:
			matches[i].Why = "No time on MCS: upload the times first"
		case r.Seconds-run.Total > 0.0005 || run.Total-r.Seconds > 0.0005:
			matches[i] = videoMatch{r.ID, fmt.Sprintf("MCS has %s: upload the times again first", clock(r.Seconds))}
		case r.VideoURL != "":
			matches[i] = videoMatch{r.ID, "Already has a video on MCS"}
		default:
			matches[i].ResultID = r.ID
		}
	}
	return matches
}
