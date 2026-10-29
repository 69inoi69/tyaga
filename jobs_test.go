package main

import (
	"slices"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		link     string
		playlist bool
		fixed    string
		list     bool
		mix      bool
	}{
		{"https://www.youtube.com/watch?v=abc", true, "https://www.youtube.com/watch?v=abc", false, false},
		{"https://www.youtube.com/watch?v=abc&list=PLx", false, "https://www.youtube.com/watch?v=abc&list=PLx", false, false},
		{"https://www.youtube.com/watch?v=abc&list=PLx", true, "https://www.youtube.com/watch?v=abc&list=PLx", true, false},
		{"https://www.youtube.com/playlist?list=PLx", false, "https://www.youtube.com/playlist?list=PLx", true, false},
		{"https://www.youtube.com/@someone", false, "https://www.youtube.com/@someone/videos", true, false},
		{"https://youtube.com/channel/UC123/", false, "https://youtube.com/channel/UC123/videos", true, false},
		{"https://www.youtube.com/@someone/shorts", false, "https://www.youtube.com/@someone/shorts", true, false},
		{"https://m.youtube.com/watch?v=abc&list=RDabc", true, "https://m.youtube.com/watch?v=abc&list=RDabc", true, true},
		{"https://music.youtube.com/playlist?list=RDCLAK5uy_x", false, "https://music.youtube.com/playlist?list=RDCLAK5uy_x", true, false},
		{"https://youtu.be/abc?list=PLx", true, "https://youtu.be/abc?list=PLx", true, false},
		{"https://soundcloud.com/someone/sets/album", true, "https://soundcloud.com/someone/sets/album", true, false},
	}
	for _, c := range cases {
		fixed, list, mix := classify(c.link, c.playlist)
		if fixed != c.fixed || list != c.list || mix != c.mix {
			t.Errorf("classify(%q, %v) = %q %v %v, want %q %v %v", c.link, c.playlist, fixed, list, mix, c.fixed, c.list, c.mix)
		}
	}
}

// value returns what follows flag in args ("" if the flag isn't there).
func value(args []string, flag string) string {
	if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

func TestBuildArgs(t *testing.T) {
	video := &Job{URL: "https://youtu.be/abc", Options: Options{Mode: "video", Quality: 720}}
	args := buildArgs(video)
	if got := value(args, "-S"); got != "res:720" {
		t.Errorf("quality: -S %q", got)
	}
	if !slices.Contains(args, "--no-playlist") || slices.Contains(args, "--sponsorblock-remove") {
		t.Errorf("single video without sponsors: %v", args)
	}
	if args[len(args)-2] != "--" || args[len(args)-1] != video.URL {
		t.Errorf("the link must come last, after --: %v", args[len(args)-2:])
	}

	merged := &Job{URL: "https://www.youtube.com/watch?v=a&list=RDa", List: true, mix: true,
		Options: Options{Mode: "video", Merge: true, Sponsors: true}}
	args = buildArgs(merged)
	if got := value(args, "-S"); got != "vcodec:h264,res,acodec:aac" {
		t.Errorf("merge prefers H.264: -S %q", got)
	}
	if got := value(args, "--playlist-items"); got != "1:25" {
		t.Errorf("mix limit: %q", got)
	}
	if got := value(args, "--sponsorblock-remove"); got != "sponsor,selfpromo,interaction" {
		t.Errorf("video sponsors: %q", got)
	}
	if !strings.Contains(strings.Join(args, " "), "after_move:TRACK|") {
		t.Error("merge must ask yt-dlp for TRACK lines")
	}

	audio := &Job{URL: "https://www.youtube.com/playlist?list=PLx", List: true,
		Options: Options{Mode: "audio", Format: "mp3", Split: true, Merge: true, Sponsors: true}}
	args = buildArgs(audio)
	if got := value(args, "--sponsorblock-remove"); !strings.HasSuffix(got, ",music_offtopic") {
		t.Errorf("audio sponsors: %q", got)
	}
	if slices.Contains(args, "--split-chapters") {
		t.Error("no splitting by chapters when gluing into one file")
	}
	if got := value(args, "--audio-format"); got != "mp3" {
		t.Errorf("audio format: %q", got)
	}
}

func TestOnLine(t *testing.T) {
	j := &Job{}
	onLine(j, "ITEM|3|12|A | title with bars")
	if j.Title != "[3/12] A | title with bars" {
		t.Errorf("title: %q", j.Title)
	}
	onLine(j, "PROG|512|1024|1048576|65")
	if j.Progress != 50 || j.Speed != "1.0 MB/s" || j.ETA != "1:05" {
		t.Errorf("progress: %v %q %q", j.Progress, j.Speed, j.ETA)
	}
	onLine(j, "PROG|NA|NA|NA|NA") // unknown values must not break anything
	onLine(j, "FILE|/out/My list/003 - Song.m4a")
	if len(j.Files) != 1 || j.Files[0] != "003 - Song.m4a" {
		t.Errorf("files: %v", j.Files)
	}
	onLine(j, `TRACK|{"filepath": "/out/x.m4a", "title": "Song \"1\"", "artist": "Band", "playlist_title": "List"}`)
	if len(j.tracks) != 1 || j.tracks[0].Title != `Song "1"` {
		t.Errorf("tracks: %+v", j.tracks)
	}
	onLine(j, "ERROR: [youtube] abc: Sign in to confirm you're not a bot")
	if !strings.Contains(j.Error, "cookies") {
		t.Errorf("error: %q", j.Error)
	}
}

func TestEscapes(t *testing.T) {
	if got := metaEscape("a=b;c#d\\e\nf"); got != `a\=b\;c\#d\\e f` {
		t.Errorf("metaEscape: %q", got)
	}
	if got := concatEscape("It's"); got != `It'\''s` {
		t.Errorf("concatEscape: %q", got)
	}
}

func TestExpectedSum(t *testing.T) {
	list := "02f974af9f48745988fdf9eb07ba6bd1ee85a26fd240c79ba4ffec13ed32c882  a.zip\n" +
		"64bd76725aaf89def5d78ec79ea42b713324bde7a4634e115cd958f0e8ca02d2 *b.zip\n"
	if got := expectedSum(list, "b.zip"); got != "64bd76725aaf89def5d78ec79ea42b713324bde7a4634e115cd958f0e8ca02d2" {
		t.Errorf("list: %q", got)
	}
	if got := expectedSum(list, "c.zip"); got != "" {
		t.Errorf("a file that isn't listed: %q", got)
	}
	powershell := "\r\nAlgorithm : SHA256\r\nHash      : CBA84B26E0DF5AE7F74FC66B86A95B866872A86292EBD84FA1E959927EDD93BF\r\nPath      : C:\\a\\deno.zip\r\n"
	if got := expectedSum(powershell, "deno.zip"); got != "CBA84B26E0DF5AE7F74FC66B86A95B866872A86292EBD84FA1E959927EDD93BF" {
		t.Errorf("single hash: %q", got)
	}
}
