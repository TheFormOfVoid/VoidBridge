//go:build !windows

package peer

import "os/exec"

func hideWindow(*exec.Cmd) {}
