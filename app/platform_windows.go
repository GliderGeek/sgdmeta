//go:build windows

package main

import (
	"os/exec"
	"strings"
	"syscall"
)

const psFolderDialog = `
Add-Type -AssemblyName System.Windows.Forms
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$d = New-Object System.Windows.Forms.FolderBrowserDialog
$d.Description = $env:SGDMETA_PROMPT
$d.ShowNewFolderButton = $true
$owner = New-Object System.Windows.Forms.Form -Property @{TopMost = $true}
if ($d.ShowDialog($owner) -eq [System.Windows.Forms.DialogResult]::OK) {
  Write-Output $d.SelectedPath
}
`

const psFileDialog = `
Add-Type -AssemblyName System.Windows.Forms
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$d = New-Object System.Windows.Forms.OpenFileDialog
$d.Title = $env:SGDMETA_PROMPT
$d.Filter = $env:SGDMETA_FILTER
$owner = New-Object System.Windows.Forms.Form -Property @{TopMost = $true}
if ($d.ShowDialog($owner) -eq [System.Windows.Forms.DialogResult]::OK) {
  Write-Output $d.FileName
}
`

// hidden runs a command without flashing a console window.
func hidden(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return c
}

func pickFolderDialog(prompt string) (string, error) {
	c := hidden("powershell", "-NoProfile", "-STA", "-Command", psFolderDialog)
	c.Env = append(c.Environ(), "SGDMETA_PROMPT="+prompt) // no quoting issues
	out, err := c.Output()
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", errCancelled
	}
	return p, nil
}

// pickFileDialog lets the user choose a file with extension ext (without dot).
func pickFileDialog(prompt, ext string) (string, error) {
	c := hidden("powershell", "-NoProfile", "-STA", "-Command", psFileDialog)
	c.Env = append(c.Environ(), "SGDMETA_PROMPT="+prompt,
		"SGDMETA_FILTER=*."+ext+"|*."+ext+"|Alle bestanden (*.*)|*.*")
	out, err := c.Output()
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", errCancelled
	}
	return p, nil
}

func openFile(path string) error {
	return hidden("rundll32", "url.dll,FileProtocolHandler", path).Start()
}

func revealFile(path string) error {
	return exec.Command("explorer", "/select,", path).Start()
}

func detachFromLauncher() bool { return false }
