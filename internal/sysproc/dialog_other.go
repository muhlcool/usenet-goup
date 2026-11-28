//go:build !windows

package sysproc

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

func OpenDialog(mode string, en bool) (string, error) {
	isFolder := mode == "folder" || mode == "nzbdir"
	title := dialogTitle(mode, en)
	switch runtime.GOOS {
	case "darwin":
		// Der Titel kommt aus dialogTitle (feste Texte), nie aus Benutzereingaben.
		choose := "choose file"
		if mode == "nfo" {
			choose = `choose file of type {"nfo"}`
		} else if isFolder {
			choose = "choose folder"
		}
		script := "tell application \"System Events\" to activate\nPOSIX path of (" + choose + " with prompt \"" + title + "\")"
		out, err := exec.Command("osascript", "-e", script).CombinedOutput()
		if err != nil {
			if strings.Contains(string(out), "-128") {
				return "", nil // Benutzer hat abgebrochen
			}
			return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
		return NormalizePath(strings.TrimSpace(string(out))), nil
	default:
		if _, err := exec.LookPath("zenity"); err == nil {
			args := []string{"--file-selection", "--modal", "--title=" + title}
			if mode == "nfo" {
				args = append(args, "--file-filter=*.nfo *.NFO")
			} else if isFolder {
				args = append(args, "--directory")
			}
			out, err := exec.Command("zenity", args...).Output()
			if isCancelExit(err) {
				return "", nil
			}
			if err != nil {
				return "", err
			}
			return NormalizePath(strings.TrimSpace(string(out))), nil
		}
		if _, err := exec.LookPath("kdialog"); err == nil {
			args := []string{"--title", title, "--getopenfilename", "."}
			if isFolder {
				args = []string{"--title", title, "--getexistingdirectory", "."}
			}
			out, err := exec.Command("kdialog", args...).Output()
			if isCancelExit(err) {
				return "", nil
			}
			if err != nil {
				return "", err
			}
			return NormalizePath(strings.TrimSpace(string(out))), nil
		}
		return "", errors.New("kein Dateidialog gefunden (zenity oder kdialog installieren) — Pfad bitte manuell eintragen")
	}
}

// isCancelExit: zenity/kdialog beenden sich bei "Abbrechen" mit Exit-Code 1.
func isCancelExit(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == 1
}
