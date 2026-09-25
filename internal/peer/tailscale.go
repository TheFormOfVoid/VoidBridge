package peer

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// TailscalePeers lists the Tailscale IPv4 addresses of online peers using the
// tailscale CLI. It returns nil if Tailscale isn't installed or running.
func TailscalePeers() []string {
	bin := tailscaleBinary()
	if bin == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "status", "--json")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var st struct {
		Peer map[string]struct {
			Online       bool
			TailscaleIPs []string
		}
	}
	if json.Unmarshal(out, &st) != nil {
		return nil
	}
	var ips []string
	for _, p := range st.Peer {
		if !p.Online {
			continue
		}
		for _, ip := range p.TailscaleIPs {
			if len(ip) > 0 && ip[0] != 'f' && !contains(ip, ':') { // IPv4 only
				ips = append(ips, ip)
			}
		}
	}
	return ips
}

// TailscaleInstalled reports whether the tailscale CLI can be found.
func TailscaleInstalled() bool { return tailscaleBinary() != "" }

func tailscaleBinary() string {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p
	}
	if runtime.GOOS == "windows" {
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
			p := filepath.Join(os.Getenv(env), "Tailscale", "tailscale.exe")
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

func contains(s string, c byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return true
		}
	}
	return false
}
