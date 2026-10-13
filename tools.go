package main

// Поиск и автоскачивание программ, без которых ТЯГА не работает:
//   yt-dlp — сама загрузка;
//   ffmpeg — склейка видео и звука, перекодирование, обложки;
//   Deno   — yt-dlp выполняет им JavaScript YouTube.
//
// Сначала ищем в папке bin рядом с программой, потом в PATH.
// Чего нет — скачиваем в bin с официальных страниц релизов на GitHub.

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
	status string // что сейчас происходит (для страницы)
	err    string // что пошло не так
	ok     bool   // всё на месте, можно качать
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

// find ищет программу в bin, потом в PATH.
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

// asset — откуда скачивать программу под эту систему.
// zipped: в архиве лежит нужный файл (и, возможно, ещё что-то), его достаём по имени.
type asset struct {
	url    string
	zipped []string // какие файлы достать из zip; пусто — скачивается сам файл
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

// ffmpeg скачивается автоматически только на Windows: сборки для Linux упакованы
// в tar.xz, а на Linux и Mac ffmpeg проще поставить менеджером пакетов.
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

// installHint — что сказать человеку, если программу не удалось достать.
func installHint(name string) string {
	switch runtime.GOOS {
	case "darwin":
		return "Установите: brew install " + name
	case "linux":
		return "Установите через менеджер пакетов, например: sudo apt install " + name
	}
	return "Скачайте " + name + " вручную и положите в папку bin рядом с программой."
}

// setupTools находит или скачивает всё нужное. Работает в фоне, пока страница уже открыта.
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
				problems = append(problems, "Не найден "+n.name+". "+installHint(n.name))
			} else if err := download(n.name, a); err != nil {
				fmt.Println("Ошибка при скачивании "+n.name+":", err)
				problems = append(problems, "Не удалось скачать "+n.name+". Проверьте интернет и перезапустите ТЯГУ. "+installHint(n.name))
			} else {
				path = find(n.name)
			}
		}
		*n.dst = path
	}

	// Свою копию yt-dlp обновляем при каждом запуске: YouTube часто что-то меняет,
	// и старый yt-dlp перестаёт качать. Чужую (из PATH) не трогаем.
	if strings.HasPrefix(tools.ytdlp, binDir) {
		tools.setStatus("Проверяю обновления yt-dlp…")
		cmd := exec.Command(tools.ytdlp, "-U")
		hideWindow(cmd)
		cmd.Run()
	}

	tools.mu.Lock()
	tools.status = ""
	tools.err = strings.Join(problems, "\n")
	tools.ok = tools.ytdlp != "" && tools.ffmpeg != "" // без Deno некоторые ролики не скачаются, но остальное работает
	tools.mu.Unlock()
	if tools.err != "" {
		fmt.Println(tools.err)
	}
	pump() // если ссылки добавили, пока шла подготовка
}

// download скачивает программу в bin.
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
		return fmt.Errorf("сервер ответил %s", resp.Status)
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

// unzipFiles достаёт из архива нужные файлы (где бы внутри они ни лежали) прямо в bin.
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
		return fmt.Errorf("в архиве нет %s", strings.Join(want, ", "))
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

// progressReader показывает на странице и в консоли, сколько уже скачано.
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
			tools.mu.Lock() // без печати в консоль на каждый процент
			tools.status = fmt.Sprintf("Скачиваю %s… %d%% (%.0f из %.0f МБ)", p.name, pct, mb, float64(p.total)/1048576)
			tools.mu.Unlock()
		}
	}
	if err == io.EOF {
		fmt.Printf("Скачан %s (%.0f МБ)\n", p.name, mb)
	}
	return n, err
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
