// ТЯГА (TYAGA) downloads video and audio from YouTube with yt-dlp, through a page in your browser.
//
// How it works: the program starts a tiny web server on your computer and opens a page
// in the browser. The page sends links to the server, the server runs yt-dlp for them one
// at a time and reads its output, and the page asks for progress every 0.7 s.
// On Windows there's no console window: TYAGA quits by the Quit button on the page,
// or by itself once the page is closed and the downloads are finished (see app.go).
//
// By default only this computer can reach the server. The "Phone" button on the page opens
// access from the home network with a short code, so you can download from your phone.
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
	"time"
)

//go:embed index.html
var page []byte

// version is set when a release is built (see .github/workflows/release.yml).
var version = "dev"

var (
	appDir      string // folder the program lives in
	binDir      string // yt-dlp, ffmpeg and Deno get downloaded here
	cookiesFile string // optional youtube-cookies.txt next to the program
	outDir      string // where downloads are saved
)

// lanPort is a fixed port for the phone, so the address stays the same between runs.
// localPort is the same for this computer, for the "Send to TYAGA" bookmark.
const (
	lanPort   = 8765
	localPort = 8764
)

var fixedPort bool // got localPort: the bookmark works

// savedToken is the token of the local page, kept in .tyaga-token next to the program,
// because the "Send to TYAGA" bookmark has it inside. Only this computer can reach the
// local server, the token keeps other websites out. Read-only folder: a new token each time.
func savedToken() string {
	file := filepath.Join(appDir, ".tyaga-token")
	if b, err := os.ReadFile(file); err == nil {
		if t := strings.TrimSpace(string(b)); len(t) == 32 {
			if _, err := hex.DecodeString(t); err == nil {
				return t
			}
		}
	}
	t := randomToken()
	os.WriteFile(file, []byte(t), 0o600)
	return t
}

func main() {
	consoleUTF8()
	home, _ := os.UserHomeDir()
	out := flag.String("out", filepath.Join(home, "Downloads", "YouTube"), "folder for downloads")
	noBrowser := flag.Bool("no-browser", false, "don't open the browser on start")
	flag.Parse()

	appDir = programDir()
	binDir = filepath.Join(appDir, "bin")
	cookiesFile = filepath.Join(appDir, "youtube-cookies.txt")
	outDir = *out

	// Launched again while already running: just bring up the page of the running one.
	if alreadyRunning() {
		return
	}
	openLog()

	// Local server: 127.0.0.1 only. A fixed port and a token that survives restarts, so the
	// "Send to TYAGA" bookmark keeps working. If the port is busy, any free one (the bookmark
	// then doesn't work until the next start).
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		fail("Could not start the server: " + err.Error())
	}
	fixedPort = ln.Addr().(*net.TCPAddr).Port == localPort
	localToken := savedToken()
	address := fmt.Sprintf("http://%s/?t=%s", ln.Addr(), localToken)

	os.WriteFile(instanceFile(), []byte(address), 0o600)

	logf("TYAGA %s is running. Files are saved to %s", version, outDir)
	logf("If the browser didn't open: %s", address)
	logf("To quit: the Quit button on the page, or just close the page.")

	go setupTools()
	if !*noBrowser {
		openURL(address)
		go quitWhenAbandoned() // with -no-browser nobody promised to keep a page open
	}
	fail(http.Serve(ln, handler(false, func() string { return localToken })).Error())
}

// programDir is the folder with the exe. Under "go run" the exe sits in a temp folder,
// so the current folder is used instead.
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

// fail shows the error (a message box on Windows, where there's no console) and exits.
func fail(msg string) {
	logf("%s", msg)
	os.Remove(instanceFile())
	showError(msg)
	os.Exit(1)
}

func randomToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
//  Phone access
// ---------------------------------------------------------------------------

var lan struct {
	sync.Mutex
	srv      *http.Server
	code     string // a short code instead of the long token: easy to type on a phone
	url      string
	failures int // wrong codes: after 20 the access turns itself off
}

// shortCode is 6 characters without look-alikes (no 0/o, 1/l/i).
func shortCode() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 6)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// lanIP is this computer's address in the home network. The UDP "connection" sends nothing,
// it just asks the OS which address it would use to go out.
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
			logf("Phone access is off.")
		}
		return nil
	}
	if lan.srv != nil {
		return nil
	}
	ip := lanIP()
	if ip == "" {
		return errors.New("this computer is not connected to a network")
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", lanPort))
	if err != nil {
		ln, err = net.Listen("tcp", ":0") // the fixed port is busy, take any
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
	logf("Phone access is on: %s", lan.url)
	return nil
}

func lanState() (on bool, link string) {
	lan.Lock()
	defer lan.Unlock()
	return lan.srv != nil, lan.url
}

// lanFailure counts wrong codes, so nobody can guess the code forever.
func lanFailure() {
	lan.Lock()
	lan.failures++
	tooMany := lan.failures >= 20
	lan.Unlock()
	if tooMany {
		logf("Too many wrong codes from other devices.")
		go setLAN(false)
	}
}

// ---------------------------------------------------------------------------
//  Server
// ---------------------------------------------------------------------------

// handler serves one entrance: local (this computer) or network (the phone).
func handler(remote bool, token func() string) http.Handler {
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The page itself is served to anyone: there is nothing secret in it, the token comes in the URL.
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(page)
			return
		}

		if !strings.HasPrefix(r.URL.Path, "/api/") { // favicon and such don't count as break-in attempts
			http.NotFound(w, r)
			return
		}

		// Everything else only for the page that knows the token. Normal requests send it in a
		// header (another website can't do that), file download links send it in the URL.
		got := r.Header.Get("X-Token")
		if r.URL.Path == "/api/file" || r.URL.Path == "/api/bye" { // links and sendBeacon can't send headers
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
				pageSeen()
				status, problem, ready := tools.state()
				on, link := lanState()
				mu.Lock()
				defer mu.Unlock()
				state := map[string]any{
					"version": version, "ready": ready, "setup": status, "problem": problem,
					"out": outDir, "remote": remote, "jobs": jobs,
				}
				if !remote { // the link with the code is shown only on the computer itself
					state["lan"] = map[string]any{"on": on, "url": link}
					state["fixedPort"] = fixedPort
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
			Text string
			Options
			ID int
			N  int // file number within a download
			On bool
		}
		json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)

		switch r.URL.Path {
		case "/api/add":
			links := strings.Fields(body.Text)
			if len(links) == 0 {
				writeJSON(w, map[string]string{"error": "Add at least one link."})
				return
			}
			for _, l := range links {
				u, err := url.Parse(l)
				if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
					writeJSON(w, map[string]string{"error": "This doesn't look like a link: " + l})
					return
				}
			}
			o := body.Options // only known values get through
			if o.Mode != "audio" {
				o.Mode = "video"
			}
			if o.Format != "mp3" {
				o.Format = "m4a"
			}
			if o.Quality != 1080 && o.Quality != 720 && o.Quality != 480 {
				o.Quality = 0
			}
			if _, _, ok := cutRange(o.From, o.To); !ok {
				writeJSON(w, map[string]string{"error": "The fragment times look wrong. Examples: 1:20, 0:45, 1:02:03; \"to\" after \"from\"."})
				return
			}
			addJobs(links, o)
		case "/api/cancel":
			cancelJob(body.ID)
		case "/api/clear":
			clearFinished()
		case "/api/bye":
			pageClosed()
		case "/api/quit":
			if remote { // a phone can't switch off the computer's TYAGA
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			writeJSON(w, map[string]bool{"ok": true})
			go func() { time.Sleep(200 * time.Millisecond); quit() }() // let the answer reach the page first
			return
		case "/api/open":
			if !remote { // opening the folder from a phone makes no sense: it would open on the computer
				os.MkdirAll(outDir, 0o755)
				openFolder(outDir)
			}
		case "/api/reveal":
			if path, ok := jobFile(body.ID, body.N); ok && !remote && fileExists(path) {
				revealFile(path)
			}
		case "/api/lan":
			if remote {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if err := setLAN(body.On); err != nil {
				writeJSON(w, map[string]string{"error": "Could not turn on phone access: " + err.Error()})
				return
			}
		default:
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
}
