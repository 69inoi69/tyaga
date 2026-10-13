package main

// Things that work differently on Windows.

import (
	"os/exec"
	"strconv"
	"syscall"
)

const createNoWindow = 0x08000000

// hideWindow runs yt-dlp and ffmpeg without extra black console windows.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// killTree stops a process together with its children (yt-dlp starts ffmpeg).
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	k := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
	hideWindow(k)
	k.Run()
}

// openURL opens a link in the default browser.
func openURL(url string) {
	exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

// openFolder opens a folder in Explorer.
func openFolder(dir string) {
	exec.Command("explorer.exe", dir).Start()
}

// consoleUTF8 makes non-Latin text in the console readable instead of mojibake.
func consoleUTF8() {
	syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleOutputCP").Call(65001)
}
