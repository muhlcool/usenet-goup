//go:build !windows

package sysproc

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

func OpenDialog(mode string) (string, error) {
	isFolder := mode == "folder" || mode == "nzbdir"
	switch runtime.GOOS {
	case "darwin":
		script := `tell application "System Events" to activate
POSIX path of (choose file with prompt "Datei auswählen")`
		if mode == "nfo" {
			script = `tell application "System Events" to activate
POSIX path of (choose file of type {"nfo"} with prompt "NFO-Datei auswählen")`
		} else if mode == "cover" {
			script = `tell application "System Events" to activate
POSIX path of (choose file of type {"public.image"} with prompt "Cover / Bild auswählen")`
		} else if isFolder {
			script = `tell application "System Events" to activate
POSIX path of (choose folder with prompt "Ordner auswählen")`
		}
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
			args := []string{"--file-selection", "--modal", "--title=Datei auswählen"}
			if mode == "nfo" {
				args = append(args, "--file-filter=*.nfo *.NFO")
			} else if mode == "cover" {
				args = append(args, "--file-filter=*.jpg *.jpeg *.png *.webp")
			} else if isFolder {
				args = []string{"--file-selection", "--directory", "--modal", "--title=Ordner auswählen"}
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
			args := []string{"--getopenfilename", "."}
			if isFolder {
				args = []string{"--getexistingdirectory", "."}
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
