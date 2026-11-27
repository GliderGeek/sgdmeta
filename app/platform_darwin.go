//go:build darwin

package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func pickFolderDialog(prompt string) (string, error) {
	return osascriptPick(`choose folder with prompt "` + strings.ReplaceAll(prompt, `"`, `'`) + `"`)
}

// pickFileDialog lets the user choose a file with extension ext (without dot).
func pickFileDialog(prompt, ext string) (string, error) {
	return osascriptPick(`choose file with prompt "` + strings.ReplaceAll(prompt, `"`, `'`) +
		`" of type {"` + ext + `"}`)
}

func osascriptPick(choose string) (string, error) {
	out, err := exec.Command("osascript", "-e", "POSIX path of ("+choose+")").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && strings.Contains(string(ee.Stderr), "-128") {
			return "", errCancelled // user pressed Cancel
		}
		return "", err
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", errCancelled
	}
	return p, nil
}

// detachFromLauncher: when started from a .app bundle, macOS expects the
// process to answer Apple Events (e.g. "reopen" on a second double-click). A
// plain Go program doesn't, so macOS reports "not responding". Instead, start
// a detached copy of ourselves and let the launched process exit right away.
// Returns true if the caller should exit.
func detachFromLauncher() bool {
	if os.Getenv("SGDMETA_CHILD") != "" {
		return false
	}
	exe, err := os.Executable()
	if err != nil || !strings.Contains(exe, ".app/Contents/MacOS/") {
		return false
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), "SGDMETA_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start() == nil
}

func openFile(path string) error   { return exec.Command("open", path).Start() }
func revealFile(path string) error { return exec.Command("open", "-R", path).Start() }
