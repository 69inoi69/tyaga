//go:build !windows

package main

// Things that work differently on Linux and macOS.

import (
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
)

// hideWindow puts the process into its own group, so yt-dlp can be stopped together with ffmpeg.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree stops the whole process group.
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// openURL and openFolder open a link or a folder in the default program.
func openURL(url string)    { openWith(url) }
func openFolder(dir string) { openWith(dir) }

// revealFile shows the file in the file manager: selected on macOS and in Linux file managers
// that support it (most do, through D-Bus), otherwise just its folder.
func revealFile(path string) {
	if runtime.GOOS == "darwin" {
		exec.Command("open", "-R", path).Start()
		return
	}
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	err := exec.Command("dbus-send", "--session", "--dest=org.freedesktop.FileManager1", "--type=method_call",
		"/org/freedesktop/FileManager1", "org.freedesktop.FileManager1.ShowItems",
		"array:string:"+uri, "string:").Run()
	if err != nil {
		openWith(filepath.Dir(path))
	}
}

func openWith(target string) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	exec.Command(name, target).Start()
}

func consoleUTF8() {}

// showError: the error is already printed to the terminal, wait so it can be read.
func showError(msg string) {
	fmt.Println("Press Enter to close.")
	fmt.Scanln()
}
