package main

// Finding and auto-downloading the programs TYAGA can't work without:
//   yt-dlp — the downloading itself;
//   ffmpeg — merging video and audio, converting, covers;
//   Deno   — yt-dlp uses it to run YouTube's JavaScript.
//
// First look in the bin folder next to the program, then in PATH.
// Whatever is missing gets downloaded into bin from the official GitHub release pages.

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

type toolSet struct {
	ytdlp, ffmpeg, deno string

	mu     sync.Mutex
	status string // what's happening right now (for the page)
	err    string // what went wrong
	ok     bool   // everything is in place, downloads can start
}

var tools toolSet

func (t *toolSet) ready() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ok
}

func (t *toolSet) setStatus(s string) {
	t.mu.Lock()
	t.status = s
	t.mu.Unlock()
	if s != "" {
		fmt.Println(s)
	}
}

func (t *toolSet) state() (status, err string, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status, t.err, t.ok
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// find looks for a program in bin, then in PATH.
func find(name string) string {
	local := filepath.Join(binDir, exeName(name))
	if fileExists(local) {
		return local
	}
	if p, err := exec.LookPath(name); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	return ""
}

// asset is where to download a program for this system from.
// zipped: the archive contains the needed file (and maybe more), it's extracted by name.
type asset struct {
	url    string
	zipped []string // which files to extract from the zip; empty means the download is the file itself
}

func ytdlpAsset() (asset, bool) {
	base := "https://github.com/yt-dlp/yt-dlp/releases/latest/download/"
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64", "windows/arm64":
		return asset{url: base + "yt-dlp.exe"}, true
	case "linux/amd64":
		return asset{url: base + "yt-dlp_linux"}, true
	case "linux/arm64":
		return asset{url: base + "yt-dlp_linux_aarch64"}, true
	case "darwin/amd64", "darwin/arm64":
		return asset{url: base + "yt-dlp_macos"}, true
	}
	return asset{}, false
}

func denoAsset() (asset, bool) {
	base := "https://github.com/denoland/deno/releases/latest/download/"
	file := map[string]string{
		"windows/amd64": "deno-x86_64-pc-windows-msvc.zip",
		"windows/arm64": "deno-aarch64-pc-windows-msvc.zip",
		"linux/amd64":   "deno-x86_64-unknown-linux-gnu.zip",
		"linux/arm64":   "deno-aarch64-unknown-linux-gnu.zip",
		"darwin/amd64":  "deno-x86_64-apple-darwin.zip",
		"darwin/arm64":  "deno-aarch64-apple-darwin.zip",
	}[runtime.GOOS+"/"+runtime.GOARCH]
	if file == "" {
		return asset{}, false
	}
	return asset{url: base + file, zipped: []string{exeName("deno")}}, true
}

// ffmpeg is downloaded automatically only on Windows: the Linux builds come as tar.xz,
// and on Linux and Mac it's easier to install ffmpeg with a package manager.
func ffmpegAsset() (asset, bool) {
	base := "https://github.com/yt-dlp/FFmpeg-Builds/releases/download/latest/"
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		return asset{url: base + "ffmpeg-master-latest-win64-gpl.zip", zipped: []string{"ffmpeg.exe", "ffprobe.exe"}}, true
	case "windows/arm64":
		return asset{url: base + "ffmpeg-master-latest-winarm64-gpl.zip", zipped: []string{"ffmpeg.exe", "ffprobe.exe"}}, true
	}
	return asset{}, false
}

// installHint is what to tell the user if a program couldn't be obtained.
func installHint(name string) string {
	switch runtime.GOOS {
	case "darwin":
		return "Install it: brew install " + name
	case "linux":
		return "Install it with your package manager, e.g.: sudo apt install " + name
	}
	return "Download " + name + " manually and put it into the bin folder next to the program."
}

// setupTools finds or downloads everything needed. Runs in the background while the page is already open.
func setupTools() {
	need := []struct {
		name  string
		dst   *string
		asset func() (asset, bool)
	}{
		{"yt-dlp", &tools.ytdlp, ytdlpAsset},
		{"ffmpeg", &tools.ffmpeg, ffmpegAsset},
		{"deno", &tools.deno, denoAsset},
	}
	var problems []string
	for _, n := range need {
		path := find(n.name)
		if path == "" {
			if a, ok := n.asset(); !ok {
				problems = append(problems, n.name+" not found. "+installHint(n.name))
			} else if err := download(n.name, a); err != nil {
				fmt.Println("Error downloading "+n.name+":", err)
				problems = append(problems, "Could not download "+n.name+". Check your internet and restart TYAGA. "+installHint(n.name))
			} else {
				path = find(n.name)
			}
		}
		*n.dst = path
	}

	// Our own copy of yt-dlp is updated on every start: YouTube changes things often,
	// and an old yt-dlp stops working. A system one (from PATH) is left alone.
	if strings.HasPrefix(tools.ytdlp, binDir) {
		tools.setStatus("Checking for yt-dlp updates…")
		cmd := exec.Command(tools.ytdlp, "-U")
		hideWindow(cmd)
		cmd.Run()
	}

	tools.mu.Lock()
	tools.status = ""
	tools.err = strings.Join(problems, "\n")
	tools.ok = tools.ytdlp != "" && tools.ffmpeg != "" // without Deno some videos won't download, but the rest works
	tools.mu.Unlock()
	if tools.err != "" {
		fmt.Println(tools.err)
	}
	pump() // in case links were added during setup
}

// download downloads a program into bin.
func download(name string, a asset) error {
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(binDir, name+"-*.part")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	resp, err := http.Get(a.url)
	if err != nil {
		tmp.Close()
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		tmp.Close()
		return fmt.Errorf("server replied %s", resp.Status)
	}
	_, err = io.Copy(tmp, &progressReader{r: resp.Body, total: resp.ContentLength, name: name})
	tmp.Close()
	if err != nil {
		return err
	}

	if len(a.zipped) == 0 {
		dst := filepath.Join(binDir, exeName(name))
		if err := os.Rename(tmp.Name(), dst); err != nil {
			return err
		}
		return os.Chmod(dst, 0o755)
	}
	return unzipFiles(tmp.Name(), a.zipped)
}

// unzipFiles extracts the needed files from the archive (wherever they are inside) right into bin.
func unzipFiles(archive string, want []string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	found := 0
	for _, f := range zr.File {
		base := filepath.Base(f.Name)
		for _, w := range want {
			if base != w || f.FileInfo().IsDir() {
				continue
			}
			if err := extract(f, filepath.Join(binDir, w)); err != nil {
				return err
			}
			found++
		}
	}
	if found == 0 {
		return fmt.Errorf("the archive has no %s", strings.Join(want, ", "))
	}
	return nil
}

func extract(f *zip.File, dst string) error {
	r, err := f.Open()
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

// progressReader shows on the page and in the console how much has been downloaded.
type progressReader struct {
	r           io.Reader
	total, done int64
	name        string
	lastPct     int64
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	mb := float64(p.done) / 1048576
	if p.total > 0 {
		pct := p.done * 100 / p.total
		if pct != p.lastPct {
			p.lastPct = pct
			tools.mu.Lock() // no console print for every percent
			tools.status = fmt.Sprintf("Downloading %s… %d%% (%.0f of %.0f MB)", p.name, pct, mb, float64(p.total)/1048576)
			tools.mu.Unlock()
		}
	}
	if err == io.EOF {
		fmt.Printf("Downloaded %s (%.0f MB)\n", p.name, mb)
	}
	return n, err
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
