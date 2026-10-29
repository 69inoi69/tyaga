package main

// Gluing a downloaded playlist into one file: one long audio or video with chapters,
// one chapter per playlist item (named after the video).
//
// If every item has the same format (codec, resolution, sample rate…), the pieces are stuck
// together as they are, without re-encoding: fast and lossless. Otherwise everything is
// re-encoded to one common format, which takes longer, especially for video.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// track is one downloaded playlist item, as yt-dlp reports it (the TRACK| line).
type track struct {
	Filepath      string `json:"filepath"`
	Title         string `json:"title"`
	Artist        string `json:"artist"`
	PlaylistTitle string `json:"playlist_title"`
}

// probeInfo is the part of ffprobe's answer that matters here.
type probeInfo struct {
	duration float64
	audio    string // codec, sample rate, channels
	video    string // codec, profile, size, frame rate, pixel format ("" for audio files)
	width    int
	height   int
	fps      float64
}

func ffprobePath() string {
	p := filepath.Join(filepath.Dir(tools.ffmpeg), exeName("ffprobe"))
	if fileExists(p) {
		return p
	}
	if p, err := exec.LookPath("ffprobe"); err == nil {
		return p
	}
	return ""
}

func probe(ffprobe, file string) (probeInfo, error) {
	cmd := exec.Command(ffprobe, "-v", "error", "-of", "json",
		"-show_entries", "format=duration:stream=codec_type,codec_name,profile,width,height,r_frame_rate,sample_rate,channels,pix_fmt:stream_disposition=attached_pic",
		file)
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return probeInfo{}, err
	}
	var r struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			Type        string `json:"codec_type"`
			Codec       string `json:"codec_name"`
			Profile     string `json:"profile"`
			Rate        string `json:"r_frame_rate"`
			SampleRate  string `json:"sample_rate"`
			PixFmt      string `json:"pix_fmt"`
			Width       int    `json:"width"`
			Height      int    `json:"height"`
			Channels    int    `json:"channels"`
			Disposition struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return probeInfo{}, err
	}

	info := probeInfo{duration: num(r.Format.Duration)}
	if math.IsNaN(info.duration) || info.duration <= 0 {
		return info, errors.New("unknown duration")
	}
	for _, s := range r.Streams {
		switch {
		case s.Type == "audio" && info.audio == "":
			info.audio = fmt.Sprintf("%s/%s/%d", s.Codec, s.SampleRate, s.Channels)
		case s.Type == "video" && s.Disposition.AttachedPic == 0 && info.video == "":
			info.video = fmt.Sprintf("%s/%s/%dx%d/%s/%s", s.Codec, s.Profile, s.Width, s.Height, s.Rate, s.PixFmt)
			info.width, info.height = s.Width, s.Height
			if a, b, ok := strings.Cut(s.Rate, "/"); ok && num(b) > 0 {
				info.fps = num(a) / num(b)
			}
		}
	}
	if info.audio == "" && info.video == "" {
		return info, errors.New("no audio or video inside")
	}
	return info, nil
}

// metaEscape escapes a value for an ffmetadata file.
func metaEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "=", `\=`, ";", `\;`, "#", `\#`, "\n", " ", "\r", "")
	return r.Replace(s)
}

// concatEscape escapes a file name for the concat list: inside '…' a ' is written as '\”.
func concatEscape(s string) string {
	return strings.ReplaceAll(s, "'", `'\''`)
}

// freeName returns path, or "path (2)", "path (3)"… if the file already exists.
func freeName(base, ext string) string {
	p := base + ext
	for i := 2; fileExists(p); i++ {
		p = fmt.Sprintf("%s (%d)%s", base, i, ext)
	}
	return p
}

// mergePlaylist glues the downloaded items of job j into one file and deletes the pieces.
// Returns the path of the new file.
func mergePlaylist(j *Job, tracks []track) (string, error) {
	ffprobe := ffprobePath()
	if ffprobe == "" {
		return "", errors.New("ffprobe not found (it comes with ffmpeg)")
	}

	var infos []probeInfo
	var kept []track
	var total float64
	for _, t := range tracks {
		info, err := probe(ffprobe, t.Filepath)
		if err != nil {
			continue // a broken or missing piece is skipped instead of breaking the whole thing
		}
		infos, kept = append(infos, info), append(kept, t)
		total += info.duration
	}
	if len(kept) == 0 {
		return "", errors.New("nothing to merge: no downloaded files were found")
	}
	tracks = kept

	// The pieces lie in the playlist folder; the glued file goes next to the folder, with the same name.
	dir := filepath.Dir(tracks[0].Filepath)
	ext := filepath.Ext(tracks[0].Filepath)
	out := freeName(dir, ext)
	tmp := filepath.Join(filepath.Dir(dir), "~"+filepath.Base(out)) // same extension: ffmpeg picks the format by it

	// Tags and chapters: one chapter per piece, named after the video.
	title := tracks[0].PlaylistTitle
	if title == "" || title == "NA" {
		title = filepath.Base(dir)
	}
	artist := tracks[0].Artist
	for _, t := range tracks {
		if t.Artist != artist { // different artists — a compilation, no common artist
			artist = ""
			break
		}
	}
	meta := filepath.Join(dir, "~chapters.txt")
	var b strings.Builder
	b.WriteString(";FFMETADATA1\n")
	b.WriteString("title=" + metaEscape(title) + "\n")
	b.WriteString("album=" + metaEscape(title) + "\n")
	if artist != "" && artist != "NA" {
		b.WriteString("artist=" + metaEscape(artist) + "\n")
	}
	var start float64
	for i, t := range tracks {
		end := start + infos[i].duration
		name := t.Title
		if name == "" || name == "NA" {
			name = strings.TrimSuffix(filepath.Base(t.Filepath), ext)
		}
		fmt.Fprintf(&b, "[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n",
			int64(math.Round(start*1000)), int64(math.Round(end*1000)), metaEscape(name))
		start = end
	}
	if err := os.WriteFile(meta, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	defer os.Remove(meta)

	// step runs one ffmpeg command. Its progress counts from `done` seconds out of `total`,
	// so the bar goes smoothly through all the steps.
	step := func(args []string, label string, done float64) error {
		args = append([]string{"-y", "-hide_banner", "-loglevel", "error", "-nostats", "-progress", "pipe:1"}, args...)
		cmd := exec.Command(tools.ffmpeg, args...)
		hideWindow(cmd)
		stdout, _ := cmd.StdoutPipe()
		var stderr strings.Builder
		cmd.Stderr = &stderr

		mu.Lock()
		if j.Status != "running" { // cancelled while the previous step was finishing
			mu.Unlock()
			return errCancelled
		}
		if err := cmd.Start(); err != nil {
			mu.Unlock()
			return err
		}
		running = cmd // so the × button can stop the gluing too
		j.Title, j.Speed, j.ETA = label, "", ""
		j.Progress = math.Min(99.9, done/total*100)
		mu.Unlock()

		// ffmpeg's -progress: "out_time_us=…" lines tell how far it got.
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "out_time_us="); ok {
				if us, err := strconv.ParseFloat(v, 64); err == nil && us > 0 {
					mu.Lock()
					j.Progress = math.Min(99.9, (done+us/1e6)/total*100)
					mu.Unlock()
				}
			}
		}
		err := cmd.Wait()

		mu.Lock()
		cancelled := j.Status != "running"
		mu.Unlock()
		if cancelled {
			return errCancelled
		}
		if err != nil {
			msg := strings.TrimSpace(stderr.String())
			if i := strings.LastIndex(msg, "\n"); i >= 0 {
				msg = msg[i+1:]
			}
			return fmt.Errorf("ffmpeg could not merge the files: %s", msg)
		}
		return nil
	}

	// glue sticks the pieces together as they are (they must all have the same format)
	// and adds the tags, the chapters and, for audio, the cover of the first track.
	glue := func(pieces []string, label string, done float64) error {
		list := filepath.Join(dir, "~concat.txt")
		var b strings.Builder
		b.WriteString("ffconcat version 1.0\n")
		for _, p := range pieces {
			b.WriteString("file '" + concatEscape(filepath.Base(p)) + "'\n") // relative to the list: same folder
		}
		if err := os.WriteFile(list, []byte(b.String()), 0o644); err != nil {
			return err
		}
		defer os.Remove(list)

		args := []string{"-f", "concat", "-safe", "0", "-i", list, "-i", meta}
		if j.Mode == "audio" {
			args = append(args, "-i", tracks[0].Filepath,
				"-map", "0:a:0", "-map", "2:v?", "-c", "copy", "-disposition:v", "attached_pic")
			if ext == ".mp3" {
				args = append(args, "-id3v2_version", "3")
			}
		} else {
			args = append(args, "-map", "0:v:0", "-map", "0:a:0?", "-c", "copy", "-movflags", "+faststart")
		}
		args = append(args, "-map_metadata", "1", "-map_chapters", "1", tmp)
		err := step(args, label, done)
		if err != nil {
			os.Remove(tmp)
		}
		return err
	}

	// Same format everywhere (the usual case: one channel, one quality) — glue right away.
	same := true
	for _, in := range infos[1:] {
		if in.audio != infos[0].audio || in.video != infos[0].video {
			same = false
			break
		}
	}
	var originals []string
	for _, t := range tracks {
		originals = append(originals, t.Filepath)
	}
	var pieces []string // re-encoded copies, if it comes to that
	defer func() {
		for _, p := range pieces {
			os.Remove(p)
		}
	}()
	err := errors.New("")
	if same {
		err = glue(originals, "Merging into one file…", 0)
	}

	// Different formats (or gluing as-is failed): first bring every piece to one common format,
	// one by one, then glue those. Re-encoding everything in one go instead leaves gaps
	// where the sample rate changes.
	if err != nil && err != errCancelled {
		var encode []string
		if j.Mode == "audio" {
			if ext == ".mp3" {
				encode = []string{"-c:a", "libmp3lame", "-q:a", "0"}
			} else {
				encode = []string{"-c:a", "aac", "-b:a", "256k"}
			}
			encode = append(encode, "-ar", "44100", "-ac", "2")
		} else {
			// Common frame: the size of the biggest video; the rest are scaled to fit, with black bars.
			w, h, fps := 0, 0, 0.0
			for _, in := range infos {
				if in.width*in.height > w*h {
					w, h = in.width, in.height
				}
				fps = math.Max(fps, in.fps)
			}
			w, h = w/2*2, h/2*2 // H.264 wants even sizes
			if fps <= 0 || fps > 60 {
				fps = 30
			}
			vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=%g,format=yuv420p",
				w, h, w, h, math.Round(fps*1000)/1000)
			encode = []string{"-vf", vf, "-c:v", "libx264", "-preset", "veryfast", "-crf", "20",
				"-c:a", "aac", "-b:a", "192k", "-ar", "48000", "-ac", "2"}
		}

		label := "Merging into one file (re-encoding, this takes a while)…"
		var done float64
		err = nil
		for i, t := range tracks {
			p := filepath.Join(dir, fmt.Sprintf("~%04d%s", i+1, ext))
			pieces = append(pieces, p)
			args := []string{"-i", t.Filepath, "-map", "0:a:0"}
			if j.Mode == "video" {
				args = []string{"-i", t.Filepath, "-map", "0:v:0", "-map", "0:a:0?"}
			}
			args = append(args, encode...)
			args = append(args, "-map_metadata", "-1", "-map_chapters", "-1", p)
			if err = step(args, label, done); err != nil {
				break
			}
			done += infos[i].duration
		}
		if err == nil {
			err = glue(pieces, label, total*0.99)
		}
	}
	if err != nil {
		return "", err
	}
	if err := os.Rename(tmp, out); err != nil {
		os.Remove(tmp)
		return "", err
	}

	// The pieces aren't needed anymore. The folder is removed only if nothing else is left in it.
	for _, t := range tracks {
		os.Remove(t.Filepath)
	}
	for _, p := range pieces {
		os.Remove(p)
	}
	os.Remove(meta)
	os.Remove(dir)
	return out, nil
}

var errCancelled = errors.New("cancelled")
