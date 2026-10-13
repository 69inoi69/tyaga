package main

// The download queue: builds the yt-dlp command, runs downloads one at a time,
// reads the output and tags the tracks that were split by chapters.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Job is one download = one link (a video or a whole playlist).
type Job struct {
	ID       int      `json:"id"`
	URL      string   `json:"url"`
	Mode     string   `json:"mode"`     // "video" or "audio"
	Playlist bool     `json:"playlist"` // download the whole playlist if the link points to one
	Split    bool     `json:"split"`    // audio: split into separate tracks by chapters
	Edit     bool     `json:"edit"`     // video: H.264 + AAC for video editors
	Format   string   `json:"format"`   // audio: "m4a" or "mp3"
	Status   string   `json:"status"`   // queued, running, done, error, cancelled
	Title    string   `json:"title"`    // title of the current video, numbered if it's a playlist
	Progress float64  `json:"progress"` // 0–100 for the current file
	Speed    string   `json:"speed"`
	ETA      string   `json:"eta"`
	Error    string   `json:"error"`
	Files    []string `json:"files"` // names of finished files (to download them to a phone)

	paths  []string    // full paths of finished files
	splits []splitInfo // videos that were split by chapters
}

// splitInfo is what yt-dlp reports about a video that was split by chapters.
type splitInfo struct {
	Filepath string `json:"filepath"`
	Album    string `json:"album"`
	Title    string `json:"title"`
	Chapters []struct {
		Title string `json:"title"`
	} `json:"chapters"`
}

var (
	mu      sync.Mutex // guards everything below
	jobs    = []*Job{}
	nextID  = 1
	running *exec.Cmd // the current yt-dlp process; only one download runs at a time
	current *Job
)

// Track cover: the video thumbnail is cropped to a centered square. On YouTube, music usually
// has the album cover as a square in the middle of a 16:9 frame, so the crop leaves exactly that.
//
// The crop only happens during conversion, and yt-dlp doesn't convert a thumbnail that is
// already JPEG. Hence the rule "jpg>png/jpg": JPEG → PNG, everything else (WebP) → JPEG.
const (
	coverFormat = "jpg>png/jpg"
	squareCover = `ThumbnailsConvertor+FFmpeg_o:-vf crop="'if(gt(ih,iw),iw,ih)':'if(gt(iw,ih),ih,iw)'"`
)

// audioTags are clean tags for music. The rules run in order, before the download,
// so the cleaned title and artist end up both in the tags and in the file name.
var audioTags = []string{
	// Title: YouTube Music tracks have an exact "track" field, better than the video title.
	"--parse-metadata", "%(track,title)s:%(title)s",
	// Artist: from YouTube Music data, otherwise the channel name.
	// The trailing "|": if nothing is found, the artist is empty instead of a literal "NA".
	"--parse-metadata", "%(artist,creator,uploader|)s:%(artist)s",
	// "Artist - Song" in the title → artist and song separately.
	"--parse-metadata", `title:(?P<artist>.+?) [-–—] (?P<title>.+)`,
	// Channel name tails: "Someone - Topic", "SomeoneVEVO".
	"--replace-in-metadata", "artist", `(?i)\s*(- topic|vevo)$`, "",
	// Junk in brackets: (Official Video), [Lyrics], (Official Audio), (HD)…
	"--replace-in-metadata", "title",
	`(?i)\s*[(\[][^)\]]*\b(official|lyrics?|audio|video|visuali[sz]er|mv|hd|hq|4k)\b[^)\]]*[)\]]`, "",
	// Year: the release date if YouTube knows it, otherwise the upload year.
	"--parse-metadata", `%(release_year,upload_date)s:(?P<meta_date>\d{4})`,
	// Drop the video description and the YouTube link from the tags.
	"--parse-metadata", ":(?P<meta_description>)",
	"--parse-metadata", ":(?P<meta_synopsis>)",
	"--parse-metadata", ":(?P<meta_purl>)",
	"--parse-metadata", ":(?P<meta_comment>)",
}

var listRe = regexp.MustCompile(`[?&]list=`)

// buildArgs builds the yt-dlp command line for a download.
func buildArgs(j *Job) []string {
	args := []string{
		// Ignore any yt-dlp settings on the computer, use only what's set here.
		"--ignore-config", "--no-plugin-dirs",
		// Don't overwrite finished files, resume unfinished ones.
		"--no-overwrites", "--continue",
		// File names that are valid on Windows and no longer than 180 characters.
		"--windows-filenames", "--trim-filenames", "180",
		// Output that's easy to parse: no colors, every update on a new line.
		"--no-colors", "--newline", "--progress", "--progress-delta", "0.5", "--encoding", "utf-8",
		"--socket-timeout", "25", "--retries", "3",
		"--embed-metadata", "--embed-chapters",
		// Skip live streams: they never end.
		"--match-filter", "!is_live",
		"--ffmpeg-location", filepath.Dir(tools.ffmpeg),
		// Service lines that onLine() parses:
		"--print", "before_dl:ITEM|%(playlist_index|)s|%(n_entries|)s|%(title)s",
		"--print", "after_move:FILE|%(filepath)s",
		"--progress-template",
		"download:PROG|%(progress.downloaded_bytes)s|%(progress.total_bytes,progress.total_bytes_estimate)s|%(progress.speed)s|%(progress.eta)s",
		"-P", outDir,
	}
	// YouTube requires running its JavaScript; yt-dlp needs Deno for that.
	if tools.deno != "" {
		args = append(args, "--js-runtimes", "deno:"+tools.deno)
	}

	// A playlist goes into its own folder with numbered files. A single video goes straight to the downloads folder.
	isList := j.Playlist && listRe.MatchString(j.URL)
	if isList {
		args = append(args, "--yes-playlist")
	} else {
		args = append(args, "--no-playlist")
	}
	single := "%(title)s"
	if j.Mode == "audio" {
		single = "%(artist,uploader&{} - |)s%(title)s" // "Artist - Song"; without an artist just "Song"
	}
	name := single
	if isList {
		name = "%(playlist_title)s/%(playlist_index)03d - %(title)s"
	}
	args = append(args, "-o", name+".%(ext)s")

	if j.Mode == "video" {
		// Best video + best audio in MP4. "For editing" prefers H.264 and AAC.
		if j.Edit {
			args = append(args, "-S", "vcodec:h264,res,acodec:aac")
		}
		args = append(args, "-f", "bv*+ba/b", "--merge-output-format", "mp4", "--remux-video", "mp4")
	} else {
		// For M4A take the AAC track right away, so it doesn't need re-encoding.
		if j.Format == "m4a" {
			args = append(args, "-f", "ba[ext=m4a]/ba/b")
		} else {
			args = append(args, "-f", "ba/b")
		}
		args = append(args, "-x", "--audio-format", j.Format, "--audio-quality", "0")
		args = append(args, "--embed-thumbnail", "--convert-thumbnails", coverFormat, "--ppa", squareCover)
		args = append(args, audioTags...)
		if isList { // track number = position in the playlist
			args = append(args, "--parse-metadata", "%(playlist_index)s:%(track_number)s")
		}
		if j.Split {
			// A whole album as one video with chapters → a folder with separate tracks appears next to it.
			args = append(args, "--split-chapters",
				"-o", "chapter:"+name+"/%(section_number)02d - %(section_title)s.%(ext)s",
				"--print", "after_move:DONE|%(.{filepath,album,title,chapters})j")
		}
	}

	if fileExists(cookiesFile) {
		args = append(args, "--cookies", cookiesFile)
	}
	return append(args, "--", j.URL) // "--" so the link can't be taken for an option
}

// clock: seconds → "m:ss".
func clock(sec float64) string {
	s := int(sec)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

var (
	signInRe  = regexp.MustCompile(`(?i)sign in|not a bot|login required`)
	tooManyRe = regexp.MustCompile(`(?i)HTTP Error 429|too many requests`)
)

// explain turns the most common yt-dlp errors into plain language.
func explain(line string) string {
	switch {
	case signInRe.MatchString(line):
		return "YouTube wants you to sign in. Put a fresh youtube-cookies.txt next to the program."
	case tooManyRe.MatchString(line):
		return "YouTube is limiting requests for now. Try again later."
	}
	return strings.TrimPrefix(line, "ERROR: ")
}

// num parses a number from yt-dlp output. Unknown values are printed as "NA" → NaN.
func num(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return math.NaN()
	}
	return f
}

// onLine parses one line of yt-dlp output and updates the download. Call with mu held.
func onLine(j *Job, line string) {
	switch {
	case strings.HasPrefix(line, "ITEM|"):
		// A new video started. The title may contain "|", so split into at most 3 parts.
		p := strings.SplitN(line[5:], "|", 3)
		if len(p) < 3 {
			return
		}
		j.Title = p[2]
		if p[0] != "" && p[0] != "NA" {
			j.Title = "[" + p[0] + "/" + p[1] + "] " + p[2]
		}
		j.Progress = 0
	case strings.HasPrefix(line, "PROG|"):
		p := strings.Split(line[5:], "|")
		if len(p) < 4 {
			return
		}
		done, total, speed, eta := num(p[0]), num(p[1]), num(p[2]), num(p[3])
		if total > 0 {
			j.Progress = math.Min(100, done/total*100)
		}
		j.Speed, j.ETA = "", ""
		if speed > 0 {
			j.Speed = fmt.Sprintf("%.1f MB/s", speed/1048576)
		}
		if eta > 0 {
			j.ETA = clock(eta)
		}
	case strings.HasPrefix(line, "FILE|"):
		path := line[5:]
		j.paths = append(j.paths, path)
		j.Files = append(j.Files, filepath.Base(path))
	case strings.HasPrefix(line, "DONE|"):
		var info splitInfo
		if json.Unmarshal([]byte(line[5:]), &info) == nil {
			j.splits = append(j.splits, info)
		}
	case strings.HasPrefix(line, "ERROR"):
		j.Error = explain(line)
	}
}

// addJobs puts links into the queue.
func addJobs(links []string, mode, format string, playlist, split, edit bool) {
	mu.Lock()
	for _, url := range links {
		jobs = append(jobs, &Job{
			ID: nextID, URL: url, Mode: mode, Format: format,
			Playlist: playlist, Split: split, Edit: edit,
			Status: "queued", Files: []string{},
		})
		nextID++
	}
	mu.Unlock()
	pump()
}

// pump starts the next download from the queue if nothing is downloading right now
// and all the needed programs are in place.
func pump() {
	mu.Lock()
	defer mu.Unlock()
	if running != nil || !tools.ready() {
		return
	}
	var j *Job
	for _, x := range jobs {
		if x.Status == "queued" {
			j = x
			break
		}
	}
	if j == nil {
		return
	}

	os.MkdirAll(outDir, 0o755)
	j.Status = "running"
	cmd := exec.Command(tools.ytdlp, buildArgs(j)...)
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1") // non-Latin titles without mojibake
	hideWindow(cmd)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		j.Status, j.Error = "error", err.Error()
		go pump()
		return
	}
	running, current = cmd, j

	// Read both normal output and error output, line by line.
	var wg sync.WaitGroup
	for _, r := range []io.Reader{stdout, stderr} {
		wg.Add(1)
		go func(r io.Reader) {
			defer wg.Done()
			sc := bufio.NewScanner(r)
			sc.Buffer(make([]byte, 64*1024), 1024*1024)
			for sc.Scan() {
				mu.Lock()
				onLine(j, sc.Text())
				mu.Unlock()
			}
		}(r)
	}

	go func() {
		wg.Wait()
		err := cmd.Wait()

		mu.Lock()
		splits := j.splits
		j.splits = nil
		if j.Status == "running" {
			j.Progress = 100 // the page shows "processing"
		}
		mu.Unlock()

		// Tag the split tracks while the download still counts as active.
		for _, s := range splits {
			tagChapters(s)
		}

		mu.Lock()
		if j.Status == "running" { // if the download was cancelled, the status is already "cancelled"
			if err == nil {
				j.Status = "done"
			} else {
				j.Status = "error"
				if j.Error == "" {
					j.Error = "yt-dlp failed: " + err.Error()
				}
			}
		}
		running, current = nil, nil
		mu.Unlock()
		pump()
	}()
}

// cancelJob removes a queued download, or stops the running one.
// The unfinished file stays and resumes if the link is added again.
func cancelJob(id int) {
	mu.Lock()
	defer mu.Unlock()
	for _, j := range jobs {
		if j.ID == id && (j.Status == "queued" || j.Status == "running") {
			j.Status = "cancelled"
			if current == j && running != nil {
				killTree(running) // yt-dlp together with the ffmpeg it started
			}
		}
	}
}

// clearFinished removes everything that is no longer downloading or waiting.
func clearFinished() {
	mu.Lock()
	defer mu.Unlock()
	kept := jobs[:0]
	for _, j := range jobs {
		if j.Status == "queued" || j.Status == "running" {
			kept = append(kept, j)
		}
	}
	jobs = kept
}

// jobFile returns the full path of a finished file — only files this program downloaded itself.
func jobFile(id, n int) (string, bool) {
	mu.Lock()
	defer mu.Unlock()
	for _, j := range jobs {
		if j.ID == id && n >= 0 && n < len(j.paths) {
			return j.paths[n], true
		}
	}
	return "", false
}

var chapterNumRe = regexp.MustCompile(`^\s*\d{1,3}\s*[.)\-–—:]\s*`)

// tagChapters tags the tracks split out of one video:
// title = chapter title, album = album (or the video title), track = "3/12".
// Artist, year and cover stay from the original file. The audio is not re-encoded.
func tagChapters(info splitInfo) {
	if info.Filepath == "" || len(info.Chapters) == 0 {
		return
	}
	dir := strings.TrimSuffix(info.Filepath, filepath.Ext(info.Filepath)) // tracks folder = file name without extension
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	album := info.Album
	if album == "" {
		album = info.Title
	}
	total := len(info.Chapters)
	for i, ch := range info.Chapters {
		prefix := fmt.Sprintf("%02d - ", i+1)
		file := ""
		for _, e := range entries {
			n := strings.ToLower(e.Name())
			if strings.HasPrefix(e.Name(), prefix) && (strings.HasSuffix(n, ".m4a") || strings.HasSuffix(n, ".mp3")) {
				file = e.Name()
				break
			}
		}
		if file == "" {
			continue
		}
		// "01. Song" / "1) Song" in a chapter title — the number is already in the tag.
		title := strings.TrimSpace(chapterNumRe.ReplaceAllString(ch.Title, ""))

		src := filepath.Join(dir, file)
		tmp := filepath.Join(dir, "~"+file) // same extension: ffmpeg picks the format by it
		// Only audio and cover (the cover is a "video" stream). After splitting, M4A also keeps
		// a chapter data stream that ffmpeg can't copy, so it's left out.
		args := []string{"-y", "-hide_banner", "-loglevel", "error", "-i", src, "-map", "0:a", "-map", "0:v?", "-c", "copy",
			"-metadata", fmt.Sprintf("track=%d/%d", i+1, total), "-metadata", "album=" + album}
		if title != "" {
			args = append(args, "-metadata", "title="+title)
		}
		if strings.HasSuffix(strings.ToLower(file), ".mp3") {
			args = append(args, "-id3v2_version", "3") // ID3v2.3 is readable everywhere, Windows included
		}
		args = append(args, tmp)

		cmd := exec.Command(tools.ffmpeg, args...)
		hideWindow(cmd)
		if cmd.Run() == nil {
			os.Rename(tmp, src)
		} else {
			os.Remove(tmp)
		}
	}
}
