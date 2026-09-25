//go:build !windows

package adb

import "os/exec"

func hideWindow(*exec.Cmd) {}
