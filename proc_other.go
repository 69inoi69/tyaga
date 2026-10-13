//go:build !windows

package main

// Things that work differently on Linux and macOS.

import (
	"os/exec"
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

func openWith(target string) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	exec.Command(name, target).Start()
}

func consoleUTF8() {}
