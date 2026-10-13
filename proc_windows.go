package main

// То, что на Windows делается по-своему.

import (
	"os/exec"
	"strconv"
	"syscall"
)

const createNoWindow = 0x08000000

// hideWindow: запускать yt-dlp и ffmpeg без лишних чёрных окон.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// killTree останавливает процесс вместе с дочерними (yt-dlp запускает ffmpeg).
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	k := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
	hideWindow(k)
	k.Run()
}

// openURL открывает ссылку в браузере по умолчанию.
func openURL(url string) {
	exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

// openFolder открывает папку в Проводнике.
func openFolder(dir string) {
	exec.Command("explorer.exe", dir).Start()
}

// consoleUTF8: чтобы кириллица в консоли не превращалась в кракозябры.
func consoleUTF8() {
	syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleOutputCP").Call(65001)
}
