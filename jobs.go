package main

// Очередь загрузок: собирает команду для yt-dlp, запускает загрузки по одной,
// читает вывод и подписывает треки, нарезанные по главам.

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

// Job — одна загрузка = одна ссылка (ролик или целый плейлист).
type Job struct {
	ID       int      `json:"id"`
	URL      string   `json:"url"`
	Mode     string   `json:"mode"`     // "video" или "audio"
	Playlist bool     `json:"playlist"` // качать весь плейлист, если ссылка на него указывает
	Split    bool     `json:"split"`    // аудио: резать по главам на отдельные треки
	Edit     bool     `json:"edit"`     // видео: H.264 + AAC для программ монтажа
	Format   string   `json:"format"`   // аудио: "m4a" или "mp3"
	Status   string   `json:"status"`   // queued, running, done, error, cancelled
	Title    string   `json:"title"`    // название текущего ролика, с номером если это плейлист
	Progress float64  `json:"progress"` // 0–100 для текущего файла
	Speed    string   `json:"speed"`
	ETA      string   `json:"eta"`
	Error    string   `json:"error"`
	Files    []string `json:"files"` // имена готовых файлов (для скачивания на телефон)

	paths  []string    // полные пути готовых файлов
	splits []splitInfo // ролики, порезанные на главы
}

// splitInfo — то, что yt-dlp сообщает о ролике, порезанном по главам.
type splitInfo struct {
	Filepath string `json:"filepath"`
	Album    string `json:"album"`
	Title    string `json:"title"`
	Chapters []struct {
		Title string `json:"title"`
	} `json:"chapters"`
}

var (
	mu      sync.Mutex // защищает всё ниже
	jobs    = []*Job{}
	nextID  = 1
	running *exec.Cmd // текущий процесс yt-dlp; одновременно качается не больше одной загрузки
	current *Job
)

// Обложка трека: превью ролика обрезается по центру до квадрата. У музыки на YouTube
// обложка альбома обычно стоит квадратом посреди кадра 16:9 — после обрезки остаётся ровно она.
//
// Обрезка срабатывает только при конвертации, а превью, которое уже в JPEG, yt-dlp
// не конвертирует. Поэтому правило «jpg>png/jpg»: JPEG → PNG, всё остальное (WebP) → JPEG.
const (
	coverFormat = "jpg>png/jpg"
	squareCover = `ThumbnailsConvertor+FFmpeg_o:-vf crop="'if(gt(ih,iw),iw,ih)':'if(gt(iw,ih),ih,iw)'"`
)

// audioTags — аккуратные теги для музыки. Правила выполняются по порядку, до скачивания,
// так что очищенные название и исполнитель попадают и в теги, и в имя файла.
var audioTags = []string{
	// Название: у треков с YouTube Music есть точное поле «трек» — оно лучше заголовка ролика.
	"--parse-metadata", "%(track,title)s:%(title)s",
	// Исполнитель: из данных YouTube Music, иначе — название канала.
	// «|» в конце: если не нашлось ничего, исполнитель пустой, а не буквальное «NA».
	"--parse-metadata", "%(artist,creator,uploader|)s:%(artist)s",
	// «Исполнитель - Песня» в названии → исполнитель отдельно, песня отдельно.
	"--parse-metadata", `title:(?P<artist>.+?) [-–—] (?P<title>.+)`,
	// Хвосты у названий каналов: «Кто-то - Topic», «КтоToVEVO».
	"--replace-in-metadata", "artist", `(?i)\s*(- topic|vevo)$`, "",
	// Мусор в скобках: (Official Video), [Lyrics], (Official Audio), (HD)…
	"--replace-in-metadata", "title",
	`(?i)\s*[(\[][^)\]]*\b(official|lyrics?|audio|video|visuali[sz]er|mv|hd|hq|4k)\b[^)\]]*[)\]]`, "",
	// Год: дата релиза, если YouTube её знает, иначе год загрузки.
	"--parse-metadata", `%(release_year,upload_date)s:(?P<meta_date>\d{4})`,
	// Убрать описание ролика и ссылку на YouTube из тегов.
	"--parse-metadata", ":(?P<meta_description>)",
	"--parse-metadata", ":(?P<meta_synopsis>)",
	"--parse-metadata", ":(?P<meta_purl>)",
	"--parse-metadata", ":(?P<meta_comment>)",
}

var listRe = regexp.MustCompile(`[?&]list=`)

// buildArgs собирает командную строку yt-dlp для загрузки.
func buildArgs(j *Job) []string {
	args := []string{
		// Не трогать чужие настройки yt-dlp на компьютере — только то, что задано здесь.
		"--ignore-config", "--no-plugin-dirs",
		// Не перезаписывать готовое, докачивать недокачанное.
		"--no-overwrites", "--continue",
		// Имена файлов, допустимые в Windows, и не длиннее 180 символов.
		"--windows-filenames", "--trim-filenames", "180",
		// Вывод, удобный для разбора: без цвета, каждое обновление с новой строки.
		"--no-colors", "--newline", "--progress", "--progress-delta", "0.5", "--encoding", "utf-8",
		"--socket-timeout", "25", "--retries", "3",
		"--embed-metadata", "--embed-chapters",
		// Прямые эфиры пропускать: они бесконечные.
		"--match-filter", "!is_live",
		"--ffmpeg-location", filepath.Dir(tools.ffmpeg),
		// Служебные строки, которые разбирает onLine():
		"--print", "before_dl:ITEM|%(playlist_index|)s|%(n_entries|)s|%(title)s",
		"--print", "after_move:FILE|%(filepath)s",
		"--progress-template",
		"download:PROG|%(progress.downloaded_bytes)s|%(progress.total_bytes,progress.total_bytes_estimate)s|%(progress.speed)s|%(progress.eta)s",
		"-P", outDir,
	}
	// YouTube требует выполнять свой JavaScript; для этого yt-dlp нужен Deno.
	if tools.deno != "" {
		args = append(args, "--js-runtimes", "deno:"+tools.deno)
	}

	// Плейлист — в отдельную папку, файлы с номерами. Одиночный ролик — прямо в папку загрузок.
	isList := j.Playlist && listRe.MatchString(j.URL)
	if isList {
		args = append(args, "--yes-playlist")
	} else {
		args = append(args, "--no-playlist")
	}
	single := "%(title)s"
	if j.Mode == "audio" {
		single = "%(artist,uploader&{} - |)s%(title)s" // «Исполнитель - Песня»; без исполнителя — просто «Песня»
	}
	name := single
	if isList {
		name = "%(playlist_title)s/%(playlist_index)03d - %(title)s"
	}
	args = append(args, "-o", name+".%(ext)s")

	if j.Mode == "video" {
		// Лучшее видео + лучший звук в MP4. «Для монтажа» — предпочесть H.264 и AAC.
		if j.Edit {
			args = append(args, "-S", "vcodec:h264,res,acodec:aac")
		}
		args = append(args, "-f", "bv*+ba/b", "--merge-output-format", "mp4", "--remux-video", "mp4")
	} else {
		// Для M4A сразу брать дорожку AAC — тогда её не придётся перекодировать.
		if j.Format == "m4a" {
			args = append(args, "-f", "ba[ext=m4a]/ba/b")
		} else {
			args = append(args, "-f", "ba/b")
		}
		args = append(args, "-x", "--audio-format", j.Format, "--audio-quality", "0")
		args = append(args, "--embed-thumbnail", "--convert-thumbnails", coverFormat, "--ppa", squareCover)
		args = append(args, audioTags...)
		if isList { // номер трека = номер в плейлисте
			args = append(args, "--parse-metadata", "%(playlist_index)s:%(track_number)s")
		}
		if j.Split {
			// Альбом одним роликом с главами → рядом появляется папка с отдельными треками.
			args = append(args, "--split-chapters",
				"-o", "chapter:"+name+"/%(section_number)02d - %(section_title)s.%(ext)s",
				"--print", "after_move:DONE|%(.{filepath,album,title,chapters})j")
		}
	}

	if fileExists(cookiesFile) {
		args = append(args, "--cookies", cookiesFile)
	}
	return append(args, "--", j.URL) // «--» — чтобы ссылку нельзя было принять за параметр
}

// clock: секунды → «м:сс».
func clock(sec float64) string {
	s := int(sec)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

var (
	signInRe  = regexp.MustCompile(`(?i)sign in|not a bot|login required`)
	tooManyRe = regexp.MustCompile(`(?i)HTTP Error 429|too many requests`)
)

// explain переводит самые частые ошибки yt-dlp на человеческий язык.
func explain(line string) string {
	switch {
	case signInRe.MatchString(line):
		return "YouTube требует авторизацию. Положите свежий файл youtube-cookies.txt рядом с программой."
	case tooManyRe.MatchString(line):
		return "YouTube временно ограничил запросы. Повторите попытку позже."
	}
	return strings.TrimPrefix(line, "ERROR: ")
}

// num разбирает число из вывода yt-dlp. Неизвестные значения он пишет как «NA» → NaN.
func num(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return math.NaN()
	}
	return f
}

// onLine разбирает одну строку вывода yt-dlp и обновляет загрузку. Вызывать под mu.
func onLine(j *Job, line string) {
	switch {
	case strings.HasPrefix(line, "ITEM|"):
		// Начался новый ролик. Название может содержать «|», поэтому режем максимум на 3 части.
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
			j.Speed = fmt.Sprintf("%.1f МБ/с", speed/1048576)
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

// addJobs ставит ссылки в очередь.
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

// pump запускает следующую загрузку из очереди, если сейчас ничего не качается
// и все нужные программы на месте.
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
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1") // кириллица без кракозябр
	hideWindow(cmd)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		j.Status, j.Error = "error", err.Error()
		go pump()
		return
	}
	running, current = cmd, j

	// Читаем и обычный вывод, и вывод ошибок — построчно.
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
			j.Progress = 100 // на странице: «обработка»
		}
		mu.Unlock()

		// Подписать нарезанные треки, пока загрузка ещё числится активной.
		for _, s := range splits {
			tagChapters(s)
		}

		mu.Lock()
		if j.Status == "running" { // если загрузку отменили, статус уже «cancelled»
			if err == nil {
				j.Status = "done"
			} else {
				j.Status = "error"
				if j.Error == "" {
					j.Error = "yt-dlp завершился с ошибкой: " + err.Error()
				}
			}
		}
		running, current = nil, nil
		mu.Unlock()
		pump()
	}()
}

// cancelJob: из очереди просто убирает, текущую загрузку — останавливает.
// Недокачанный файл остаётся и докачается, если ссылку добавить снова.
func cancelJob(id int) {
	mu.Lock()
	defer mu.Unlock()
	for _, j := range jobs {
		if j.ID == id && (j.Status == "queued" || j.Status == "running") {
			j.Status = "cancelled"
			if current == j && running != nil {
				killTree(running) // yt-dlp вместе с запущенным им ffmpeg
			}
		}
	}
}

// clearFinished убирает из списка всё, что уже не качается и не ждёт.
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

// jobFile возвращает полный путь готового файла — только из тех, что скачала сама программа.
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

// tagChapters подписывает треки, нарезанные из одного ролика:
// название = название главы, альбом = альбом (или название ролика), номер = «3/12».
// Исполнитель, год и обложка остаются от исходного файла. Звук не перекодируется.
func tagChapters(info splitInfo) {
	if info.Filepath == "" || len(info.Chapters) == 0 {
		return
	}
	dir := strings.TrimSuffix(info.Filepath, filepath.Ext(info.Filepath)) // папка с треками = имя файла без расширения
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
		// «01. Песня» / «1) Песня» в названии главы — номер и так есть в теге.
		title := strings.TrimSpace(chapterNumRe.ReplaceAllString(ch.Title, ""))

		src := filepath.Join(dir, file)
		tmp := filepath.Join(dir, "~"+file) // то же расширение — ffmpeg по нему выбирает формат
		// Только звук и обложку (обложка — это «видео»-поток). В M4A после нарезки остаётся ещё
		// служебная дорожка с главами, которую ffmpeg скопировать не может, — её не берём.
		args := []string{"-y", "-hide_banner", "-loglevel", "error", "-i", src, "-map", "0:a", "-map", "0:v?", "-c", "copy",
			"-metadata", fmt.Sprintf("track=%d/%d", i+1, total), "-metadata", "album=" + album}
		if title != "" {
			args = append(args, "-metadata", "title="+title)
		}
		if strings.HasSuffix(strings.ToLower(file), ".mp3") {
			args = append(args, "-id3v2_version", "3") // ID3v2.3 — читается везде, включая Windows
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
