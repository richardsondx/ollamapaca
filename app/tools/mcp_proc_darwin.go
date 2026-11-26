//go:build darwin

package tools

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
