package peer

import (
	"os/exec"
	"syscall"
)

// hideWindow stops a console window flashing up when a GUI app runs a CLI.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
