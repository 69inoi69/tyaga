package main

// Living without a console window. On Windows TYAGA is built as a window-less program,
// so everything that used to be the console's job is done here:
//   - messages go to tyaga.log next to the program;
//   - a second launch doesn't start a second TYAGA, it opens the page of the running one;
//   - TYAGA quits by the Quit button on the page, or by itself when the page is closed
//     and nothing is downloading anymore.

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var logFile *os.File

// logf writes a line to the console (if there is one) and to tyaga.log.
func logf(format string, a ...any) {
	line := fmt.Sprintf(format, a...)
	fmt.Println(line) // without a console this goes nowhere, and that's fine
	if logFile != nil {
		fmt.Fprintln(logFile, time.Now().Format("15:04:05"), line)
	}
}

func openLog() {
	logFile, _ = os.Create(filepath.Join(appDir, "tyaga.log")) // read-only folder: just no log
}

// instanceFile holds the address of the running TYAGA, so a second launch can find it.
func instanceFile() string { return filepath.Join(appDir, ".tyaga-running") }

// alreadyRunning: if TYAGA from this folder is already running, open its page and say true.
func alreadyRunning() bool {
	data, err := os.ReadFile(instanceFile())
	if err != nil {
		return false
	}
	address := strings.TrimSpace(string(data))
	u, err := url.Parse(address)
	if err != nil || u.Host == "" {
		return false
	}
	req, _ := http.NewRequest("GET", "http://"+u.Host+"/api/state", nil)
	req.Header.Set("X-Token", u.Query().Get("t"))
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return false // the file is left over from a TYAGA that was killed
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	openURL(address)
	return true
}

// ---------------------------------------------------------------------------
//  Quitting by itself
// ---------------------------------------------------------------------------

// The page asks for the state every 0.7 s. A background tab gets slowed down by the browser
// to about once a minute, so "nobody asked for 3 minutes" safely means the page is closed.
const noPageFor = 3 * time.Minute

var seen struct {
	sync.Mutex
	last time.Time
}

// pageSeen: some page (on the computer or a phone) just asked for the state.
func pageSeen() {
	seen.Lock()
	seen.last = time.Now()
	seen.Unlock()
}

// pageClosed: the page said goodbye (closed or reloaded). If no page asks again
// within a few seconds, it was closed for real.
func pageClosed() {
	seen.Lock()
	seen.last = time.Now().Add(-noPageFor + 8*time.Second)
	seen.Unlock()
}

// busy: something is downloading or waiting, or the first-start setup is still going.
func busy() bool {
	if status, _, _ := tools.state(); status != "" {
		return true
	}
	mu.Lock()
	defer mu.Unlock()
	for _, j := range jobs {
		if j.Status == "queued" || j.Status == "running" {
			return true
		}
	}
	return false
}

// quitWhenAbandoned quits when there's no page anymore and nothing to download:
// close the tab, and TYAGA finishes the downloads and leaves by itself.
func quitWhenAbandoned() {
	pageSeen()
	for range time.Tick(2 * time.Second) {
		seen.Lock()
		gone := time.Since(seen.last) > noPageFor
		seen.Unlock()
		if gone && !busy() {
			logf("The page is closed and nothing is downloading. Bye.")
			quit()
		}
	}
}

// quit stops the download (the unfinished file stays and resumes next time) and exits.
func quit() {
	mu.Lock()
	for _, j := range jobs {
		if j.Status == "queued" || j.Status == "running" {
			j.Status = "cancelled"
		}
	}
	if running != nil {
		killTree(running)
	}
	mu.Unlock()
	setLAN(false)
	os.Remove(instanceFile())
	if logFile != nil {
		logFile.Close()
	}
	os.Exit(0)
}
