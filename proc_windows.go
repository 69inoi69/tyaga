package main

// Things that work differently on Windows.

import (
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"
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

// revealFile opens Explorer with the file selected. The command line is written by hand:
// explorer wants /select,"path" and doesn't understand Go's usual quoting of the whole argument.
func revealFile(path string) {
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	cmd.Start()
}

// openFolder opens a folder in Explorer.
func openFolder(dir string) {
	exec.Command("explorer.exe", dir).Start()
}

// consoleUTF8 makes non-Latin text in the console readable instead of mojibake.
func consoleUTF8() {
	syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleOutputCP").Call(65001)
}

// showError shows an error in a message box: there's no console window to print it to.
func showError(msg string) {
	text, _ := syscall.UTF16PtrFromString(msg)
	title, _ := syscall.UTF16PtrFromString("TYAGA")
	const iconError = 0x10
	syscall.NewLazyDLL("user32.dll").NewProc("MessageBoxW").Call(0,
		uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), iconError)
}
