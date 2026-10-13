//go:build !windows

package main

// То, что на Linux и macOS делается по-своему.

import (
	"os/exec"
	"runtime"
	"syscall"
)

// hideWindow: отдельная группа процессов, чтобы потом остановить yt-dlp вместе с ffmpeg.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree останавливает всю группу процессов.
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// openURL и openFolder открывают ссылку или папку в программе по умолчанию.
func openURL(url string)    { openWith(url) }
func openFolder(dir string) { openWith(dir) }

func openWith(target string) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	exec.Command(name, target).Start()
}

func consoleUTF8() {}
