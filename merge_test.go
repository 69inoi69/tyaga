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
