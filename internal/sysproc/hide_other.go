//go:build !windows

package sysproc

import "os/exec"

func HideWindow(cmd *exec.Cmd) {}
