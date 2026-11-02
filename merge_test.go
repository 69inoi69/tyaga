package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMerge glues small generated "playlists" with the real ffmpeg: same format (joined as is)
// and mixed formats (re-encoded first). Skipped where ffmpeg isn't installed.
func TestMerge(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	tools.ffmpeg = ffmpeg

	gen := func(t *testing.T, path string, args ...string) {
		t.Helper()
		out, err := exec.Command(ffmpeg, append(append([]string{"-v", "error", "-y"}, args...), path)...).CombinedOutput()
		if err != nil {
			t.Fatalf("making %s: %v %s", path, err, out)
		}
	}
	audio := func(rate int, sec float64) []string {
		return []string{"-f", "lavfi", "-i", fmt.Sprintf("sine=r=%d:d=%g", rate, sec), "-c:a", "aac"}
	}
	video := func(size string, fps, sec int) []string {
		return []string{"-f", "lavfi", "-i", fmt.Sprintf("testsrc=s=%s:r=%d:d=%d", size, fps, sec),
			"-f", "lavfi", "-i", fmt.Sprintf("sine=d=%d", sec),
			"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest"}
	}

	cases := []struct {
		name, mode, ext string
		pieces          [][]string
		want            float64 // seconds
	}{
		{"same audio", "audio", ".m4a", [][]string{audio(44100, 2), audio(44100, 3)}, 5},
		{"mixed audio", "audio", ".m4a", [][]string{audio(44100, 2), audio(48000, 2)}, 4},
		{"mixed video", "video", ".mp4", [][]string{video("640x360", 30, 2), video("360x640", 25, 2)}, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "It's a list")
			os.MkdirAll(dir, 0o755)
			var tracks []track
			for i, p := range c.pieces {
				path := filepath.Join(dir, fmt.Sprintf("%03d - Piece %d%s", i+1, i+1, c.ext))
				gen(t, path, p...)
				tracks = append(tracks, track{Filepath: path, Title: fmt.Sprintf("Piece %d", i+1), PlaylistTitle: "It's a list"})
			}

			j := &Job{Options: Options{Mode: c.mode}, Status: "running"}
			out, err := mergePlaylist(j, tracks)
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Base(out) != "It's a list"+c.ext {
				t.Errorf("name: %s", out)
			}
			info, err := probe("ffprobe", out)
			if err != nil {
				t.Fatal(err)
			}
			if d := info.duration - c.want; d < -0.2 || d > 0.2 { // a gap at a joint shows up here
				t.Errorf("duration %.2f s, want about %g", info.duration, c.want)
			}
			chapters, _ := exec.Command("ffprobe", "-v", "error", "-show_chapters", "-of", "csv", out).Output()
			if n := strings.Count(string(chapters), "chapter,"); n != len(c.pieces) {
				t.Errorf("%d chapters, want %d", n, len(c.pieces))
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Error("the folder with the pieces should be gone")
			}
		})
	}
}

func TestAlbumArtist(t *testing.T) {
	tr := func(artist, uploader string) track { return track{Artist: artist, Uploader: uploader} }
	cases := []struct {
		tracks []track
		want   string
	}{
		{[]track{tr("Band", "Band - Topic"), tr("Band", "Band - Topic")}, "Band"},
		// one title had "Someone - Song" in it, the rest only the channel: still the channel's album
		{[]track{tr("", "Band - Topic"), tr("Band feat. X", "Band - Topic"), tr("", "BandVEVO")}, "Band"},
		{[]track{tr("A", ""), tr("B", ""), tr("C", "")}, "Various Artists"},
		{[]track{tr("NA", ""), tr("", "")}, ""},
	}
	for _, c := range cases {
		if got := albumArtist(c.tracks); got != c.want {
			t.Errorf("albumArtist(%+v) = %q, want %q", c.tracks, got, c.want)
		}
	}
}

// TestMergeTags: the first track has no cover, tracks arrive out of order, and the artists
// don't agree. The glued file must still get a cover, the playlist order and an artist.
func TestMergeTags(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	tools.ffmpeg = ffmpeg
	dir := filepath.Join(t.TempDir(), "Album")
	os.MkdirAll(dir, 0o755)
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command(ffmpeg, append([]string{"-v", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	cover := filepath.Join(t.TempDir(), "cover.jpg")
	run("-f", "lavfi", "-i", "color=red:s=64x64", "-frames:v", "1", cover)
	p1, p2 := filepath.Join(dir, "001 - One.m4a"), filepath.Join(dir, "002 - Two.m4a")
	run("-f", "lavfi", "-i", "sine=d=1", "-c:a", "aac", p1) // no cover
	run("-f", "lavfi", "-i", "sine=d=1", "-i", cover, "-map", "0", "-map", "1", "-c:a", "aac", "-c:v", "copy",
		"-disposition:v", "attached_pic", p2)

	tracks := []track{ // reported out of order
		{Filepath: p2, Title: "Two", Artist: "Band feat. X", Uploader: "Band - Topic", PlaylistTitle: "Album", Index: 2},
		{Filepath: p1, Title: "One", Uploader: "Band - Topic", PlaylistTitle: "Album", Index: 1},
	}
	out, err := mergePlaylist(&Job{Options: Options{Mode: "audio"}, Status: "running"}, tracks)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := exec.Command("ffprobe", "-v", "error", "-show_entries", "format_tags=artist,album_artist:stream_disposition=attached_pic",
		"-show_chapters", "-of", "compact", out).Output()
	s := string(info)
	if !strings.Contains(s, "attached_pic=1") {
		t.Errorf("no cover in the glued file:\n%s", s)
	}
	if !strings.Contains(s, "tag:artist=Band") {
		t.Errorf("no artist:\n%s", s)
	}
	if i, j := strings.Index(s, "title=One"), strings.Index(s, "title=Two"); i < 0 || j < 0 || i > j {
		t.Errorf("chapters not in playlist order:\n%s", s)
	}
}
