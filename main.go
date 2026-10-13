// ТЯГА — скачивание видео и звука с YouTube через yt-dlp, с интерфейсом в браузере.
//
// Как это работает: программа поднимает маленький веб-сервер у тебя на компьютере
// и открывает страницу в браузере. Страница отправляет ссылки серверу, сервер по одной
// запускает yt-dlp, читает его вывод, а страница раз в 0,7 с спрашивает, как дела.
// Закрыл окно консоли — сервер выключился.
//
// По умолчанию сервер слышит только этот компьютер. Кнопка «Телефон» на странице
// открывает доступ из домашней сети по короткому коду — тогда можно качать с телефона.
package main

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

//go:embed index.html
var page []byte

// version подставляется при сборке релиза (см. .github/workflows/release.yml).
var version = "dev"

var (
	appDir      string // папка, где лежит программа
	binDir      string // сюда скачиваются yt-dlp, ffmpeg, Deno
	cookiesFile string // необязательный youtube-cookies.txt рядом с программой
	outDir      string // куда складываются загрузки
)

// lanPort — постоянный порт для телефона, чтобы адрес не менялся от запуска к запуску.
const lanPort = 8765

func main() {
	consoleUTF8()
	home, _ := os.UserHomeDir()
	out := flag.String("out", filepath.Join(home, "Downloads", "YouTube"), "папка для загрузок")
	noBrowser := flag.Bool("no-browser", false, "не открывать браузер при запуске")
	flag.Parse()

	appDir = programDir()
	binDir = filepath.Join(appDir, "bin")
	cookiesFile = filepath.Join(appDir, "youtube-cookies.txt")
	outDir = *out

	// Локальный сервер: только 127.0.0.1, свободный порт выдаёт система.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fail("Не получилось запустить сервер: " + err.Error())
	}
	localToken := randomToken()
	address := fmt.Sprintf("http://%s/?t=%s", ln.Addr(), localToken)

	fmt.Println("ТЯГА " + version + " работает. Файлы сохраняются в " + outDir)
	fmt.Println("Если браузер не открылся сам: " + address)
	fmt.Println("Чтобы выключить, закройте это окно.")
	fmt.Println()

	go setupTools()
	if !*noBrowser {
		openURL(address)
	}
	fail(http.Serve(ln, handler(false, func() string { return localToken })).Error())
}

// programDir — папка с exe. При «go run» exe лежит во временной папке, тогда берём текущую.
func programDir() string {
	exe, err := os.Executable()
	if err == nil {
		exe, _ = filepath.EvalSymlinks(exe)
		dir := filepath.Dir(exe)
		if !strings.Contains(dir, "go-build") {
			return dir
		}
	}
	dir, _ := os.Getwd()
	return dir
}

func fail(msg string) {
	fmt.Println(msg)
	fmt.Println("Нажмите Enter, чтобы закрыть.")
	fmt.Scanln()
	os.Exit(1)
}

func randomToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
//  Доступ с телефона
// ---------------------------------------------------------------------------

var lan struct {
	sync.Mutex
	srv      *http.Server
	code     string // короткий код вместо длинного токена: его удобно набрать на телефоне
	url      string
	failures int // неверные коды подряд: после 20 доступ закрывается сам
}

// shortCode — 6 символов без похожих друг на друга (нет 0/o, 1/l/i).
func shortCode() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 6)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// lanIP — адрес этого компьютера в домашней сети. UDP-«соединение» ничего не отправляет,
// просто спрашивает систему, через какой адрес она пошла бы наружу.
func lanIP() string {
	if c, err := net.Dial("udp", "192.168.0.1:9"); err == nil {
		defer c.Close()
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && !a.IP.IsLoopback() {
			return a.IP.String()
		}
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.IsPrivate() && n.IP.To4() != nil {
			return n.IP.String()
		}
	}
	return ""
}

func setLAN(on bool) error {
	lan.Lock()
	defer lan.Unlock()
	if !on {
		if lan.srv != nil {
			lan.srv.Close()
			lan.srv, lan.code, lan.url = nil, "", ""
			fmt.Println("Доступ с телефона выключен.")
		}
		return nil
	}
	if lan.srv != nil {
		return nil
	}
	ip := lanIP()
	if ip == "" {
		return errors.New("компьютер не подключён к сети")
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", lanPort))
	if err != nil {
		ln, err = net.Listen("tcp", ":0") // постоянный порт занят — берём любой
		if err != nil {
			return err
		}
	}
	port := ln.Addr().(*net.TCPAddr).Port
	lan.code, lan.failures = shortCode(), 0
	lan.url = fmt.Sprintf("http://%s:%d/?t=%s", ip, port, lan.code)
	code := lan.code
	lan.srv = &http.Server{Handler: handler(true, func() string { return code })}
	go lan.srv.Serve(ln)
	fmt.Println("Доступ с телефона включён: " + lan.url)
	return nil
}

func lanState() (on bool, link string) {
	lan.Lock()
	defer lan.Unlock()
	return lan.srv != nil, lan.url
}

// lanFailure считает неверные коды. Перебирать код бесконечно не получится.
func lanFailure() {
	lan.Lock()
	lan.failures++
	tooMany := lan.failures >= 20
	lan.Unlock()
	if tooMany {
		fmt.Println("Слишком много неверных кодов с других устройств.")
		go setLAN(false)
	}
}

// ---------------------------------------------------------------------------
//  Сервер
// ---------------------------------------------------------------------------

// handler — сервер для одного входа: локального (этот компьютер) или сетевого (телефон).
func handler(remote bool, token func() string) http.Handler {
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Саму страницу отдаём всем: в ней нет ничего секретного, токен приходит в адресе.
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(page)
			return
		}

		if !strings.HasPrefix(r.URL.Path, "/api/") { // favicon и прочее — не считаем за попытку взлома
			http.NotFound(w, r)
			return
		}

		// Всё остальное — только со страницы, которая знает токен. Обычные запросы шлют его
		// в заголовке (чужой сайт так не умеет), ссылки на скачивание файла — в адресе.
		got := r.Header.Get("X-Token")
		if r.URL.Path == "/api/file" {
			got = r.URL.Query().Get("t")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(token())) != 1 {
			if remote {
				lanFailure()
			}
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		if r.Method == http.MethodGet {
			switch r.URL.Path {
			case "/api/state":
				status, problem, ready := tools.state()
				on, link := lanState()
				mu.Lock()
				defer mu.Unlock()
				state := map[string]any{
					"version": version, "ready": ready, "setup": status, "problem": problem,
					"out": outDir, "remote": remote, "jobs": jobs,
				}
				if !remote { // ссылку с кодом показываем только на самом компьютере
					state["lan"] = map[string]any{"on": on, "url": link}
				}
				writeJSON(w, state)
			case "/api/file":
				id, _ := strconv.Atoi(r.URL.Query().Get("id"))
				n, _ := strconv.Atoi(r.URL.Query().Get("n"))
				path, ok := jobFile(id, n)
				if !ok || !fileExists(path) {
					http.NotFound(w, r)
					return
				}
				name := filepath.Base(path)
				w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
				http.ServeFile(w, r, path)
			default:
				http.NotFound(w, r)
			}
			return
		}

		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Text, Mode, Format    string
			Playlist, Split, Edit bool
			ID                    int
			On                    bool
		}
		json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)

		switch r.URL.Path {
		case "/api/add":
			links := strings.Fields(body.Text)
			if len(links) == 0 {
				writeJSON(w, map[string]string{"error": "Добавьте хотя бы одну ссылку."})
				return
			}
			for _, l := range links {
				u, err := url.Parse(l)
				if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
					writeJSON(w, map[string]string{"error": "Не похоже на ссылку: " + l})
					return
				}
			}
			mode := "video"
			if body.Mode == "audio" {
				mode = "audio"
			}
			format := "m4a"
			if body.Format == "mp3" {
				format = "mp3"
			}
			addJobs(links, mode, format, body.Playlist, body.Split, body.Edit)
		case "/api/cancel":
			cancelJob(body.ID)
		case "/api/clear":
			clearFinished()
		case "/api/open":
			if !remote { // открывать папку с телефона бессмысленно: она откроется на компьютере
				os.MkdirAll(outDir, 0o755)
				openFolder(outDir)
			}
		case "/api/lan":
			if remote {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if err := setLAN(body.On); err != nil {
				writeJSON(w, map[string]string{"error": "Не получилось включить доступ с телефона: " + err.Error()})
				return
			}
		default:
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
}
