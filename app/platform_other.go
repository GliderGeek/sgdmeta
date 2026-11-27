//go:build !darwin && !windows

package main

import (
	"errors"
	"os/exec"
	"path/filepath"
)

func pickFolderDialog(string) (string, error) {
	return "", errors.New("geen systeemvenster voor mappen beschikbaar")
}

func pickFileDialog(string, string) (string, error) {
	return "", errors.New("geen systeemvenster voor bestanden beschikbaar")
}

func openFile(path string) error   { return exec.Command("xdg-open", path).Start() }
func revealFile(path string) error { return exec.Command("xdg-open", filepath.Dir(path)).Start() }

func detachFromLauncher() bool { return false }
