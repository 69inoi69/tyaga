// ТЯГА (TYAGA) downloads video and audio from YouTube with yt-dlp, through a page in your browser.
//
// How it works: the program starts a tiny web server on your computer and opens a page
// in the browser. The page sends links to the server, the server runs yt-dlp for them one
// at a time and reads its output, and the page asks for progress every 0.7 s.
// Close the console window and the server stops.
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
const lanPort = 8765

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

	// Local server: 127.0.0.1 only, the OS picks a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fail("Could not start the server: " + err.Error())
	}
	localToken := randomToken()
	address := fmt.Sprintf("http://%s/?t=%s", ln.Addr(), localToken)

	fmt.Println("TYAGA " + version + " is running. Files are saved to " + outDir)
	fmt.Println("If the browser didn't open: " + address)
	fmt.Println("Close this window to quit.")
	fmt.Println()

	go setupTools()
	if !*noBrowser {
		openURL(address)
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

func fail(msg string) {
	fmt.Println(msg)
	fmt.Println("Press Enter to close.")
	fmt.Scanln()
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
			fmt.Println("Phone access is off.")
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
	fmt.Println("Phone access is on: " + lan.url)
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
		fmt.Println("Too many wrong codes from other devices.")
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
				if !remote { // the link with the code is shown only on the computer itself
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
			Merge                 bool
			ID                    int
			On                    bool
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
			mode := "video"
			if body.Mode == "audio" {
				mode = "audio"
			}
			format := "m4a"
			if body.Format == "mp3" {
				format = "mp3"
			}
			addJobs(links, mode, format, body.Playlist, body.Split, body.Edit, body.Merge)
		case "/api/cancel":
			cancelJob(body.ID)
		case "/api/clear":
			clearFinished()
		case "/api/open":
			if !remote { // opening the folder from a phone makes no sense: it would open on the computer
				os.MkdirAll(outDir, 0o755)
				openFolder(outDir)
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
