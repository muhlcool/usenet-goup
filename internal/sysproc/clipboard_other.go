//go:build !windows

package sysproc

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// CopyTextToClipboard nutzt pbcopy (macOS) bzw. wl-copy (Wayland), xclip oder xsel (X11).
func CopyTextToClipboard(text string) error {
	var candidates [][]string
	if runtime.GOOS == "darwin" {
		candidates = [][]string{{"pbcopy"}}
	} else {
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			candidates = append(candidates, []string{"wl-copy"})
		}
		candidates = append(candidates,
			[]string{"xclip", "-selection", "clipboard"},
			[]string{"xsel", "--clipboard", "--input"},
			[]string{"wl-copy"},
		)
	}
	for _, c := range candidates {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run()
	}
	return errors.New("kein Zwischenablage-Programm gefunden (wl-copy, xclip oder xsel installieren)")
}
