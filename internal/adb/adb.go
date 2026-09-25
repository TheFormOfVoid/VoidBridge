// Package adb drives Android's adb tool so the desktop app can finish phone
// setup in one click: it grants the permissions Android only lets a computer
// grant (READ_LOGS for background clipboard detection), plus the overlay and
// battery exemptions, instead of the user typing commands.
package adb

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Package is the Android app's id.
const Package = "com.theformofvoid.voidbridge"

// Device is a phone adb can see.
type Device struct {
	Serial string `json:"serial"`
	State  string `json:"state"` // "device", "unauthorized", "offline"
	Model  string `json:"model"`
	// Wireless is true for devices connected over Wi-Fi debugging.
	Wireless bool `json:"wireless"`
}

// Step is the outcome of one setup action.
type Step struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func toolsDir() string {
	base, err := os.UserCacheDir() // %LOCALAPPDATA% on Windows
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "VoidBridge", "platform-tools")
}

func exeName() string {
	if runtime.GOOS == "windows" {
		return "adb.exe"
	}
	return "adb"
}

// Path returns the adb binary to use, or "" if none is available.
func Path() string {
	own := filepath.Join(toolsDir(), exeName())
	if _, err := os.Stat(own); err == nil {
		return own
	}
	if p, err := exec.LookPath("adb"); err == nil {
		return p
	}
	return ""
}

// Install downloads Google's platform-tools (about 7 MB) into the app's
// cache folder.
func Install(ctx context.Context) error {
	osName := map[string]string{"windows": "windows", "darwin": "darwin", "linux": "linux"}[runtime.GOOS]
	if osName == "" {
		return errors.New("unsupported OS")
	}
	url := "https://dl.google.com/android/repository/platform-tools-latest-" + osName + ".zip"
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading adb: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("downloading adb: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 100<<20))
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	dest := filepath.Dir(toolsDir()) // zip entries start with platform-tools/
	for _, f := range zr.File {
		name := filepath.Clean(f.Name)
		if !strings.HasPrefix(name, "platform-tools") || strings.Contains(name, "..") {
			continue
		}
		target := filepath.Join(dest, name)
		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(target), 0o755)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode()|0o600)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return err
		}
	}
	if Path() == "" {
		return errors.New("adb was not found in the download")
	}
	return nil
}

func run(timeout time.Duration, args ...string) (string, error) {
	bin := Path()
	if bin == "" {
		return "", errors.New("adb is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if ctx.Err() != nil {
		return s, errors.New("adb timed out")
	}
	if err != nil {
		if s == "" {
			s = err.Error()
		}
		return s, errors.New(s)
	}
	return s, nil
}

// Devices lists connected phones.
func Devices() ([]Device, error) {
	out, err := run(15*time.Second, "devices", "-l")
	if err != nil {
		return nil, err
	}
	return parseDevices(out), nil
}

func parseDevices(out string) []Device {
	var devs []Device
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || strings.HasPrefix(line, "List of") || strings.HasPrefix(line, "*") {
			continue
		}
		d := Device{Serial: f[0], State: f[1], Wireless: strings.Contains(f[0], ":") || strings.Contains(f[0], "._adb-tls-connect")}
		for _, kv := range f[2:] {
			if v, ok := strings.CutPrefix(kv, "model:"); ok {
				d.Model = strings.ReplaceAll(v, "_", " ")
			}
		}
		if d.Model == "" {
			d.Model = d.Serial
		}
		devs = append(devs, d)
	}
	return devs
}

// Pair pairs with a phone's Wireless debugging ("Pair device with pairing code").
func Pair(hostPort, code string) error {
	out, err := run(30*time.Second, "pair", strings.TrimSpace(hostPort), strings.TrimSpace(code))
	if err != nil {
		return err
	}
	if !strings.Contains(strings.ToLower(out), "successfully paired") {
		return errors.New(out)
	}
	return nil
}

// Connect connects to a paired phone's Wireless debugging address.
func Connect(hostPort string) error {
	out, err := run(20*time.Second, "connect", strings.TrimSpace(hostPort))
	if err != nil {
		return err
	}
	if strings.Contains(out, "failed") || strings.Contains(out, "cannot") {
		return errors.New(out)
	}
	return nil
}

// Setup grants VoidBridge everything it needs on the phone.
func Setup(serial string) ([]Step, error) {
	sh := func(args ...string) (string, error) {
		return run(20*time.Second, append([]string{"-s", serial, "shell"}, args...)...)
	}
	out, err := sh("pm", "list", "packages", Package)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(out, "package:"+Package) {
		return nil, errors.New("VoidBridge isn't installed on this phone yet. Install the Android app first, then try again.")
	}
	var steps []Step
	do := func(name string, optional bool, args ...string) {
		out, err := sh(args...)
		st := Step{Name: name, OK: err == nil}
		if err != nil {
			st.Error = out
		} else if strings.Contains(strings.ToLower(out), "exception") || strings.Contains(strings.ToLower(out), "error") {
			st.OK, st.Error = false, out
		}
		if !st.OK && optional {
			return
		}
		steps = append(steps, st)
	}
	do("Automatic phone → PC sync (read clipboard in the background)", false, "pm", "grant", Package, "android.permission.READ_LOGS")
	do("Display over other apps", false, "appops", "set", Package, "SYSTEM_ALERT_WINDOW", "allow")
	do("Unrestricted battery (stay connected when the screen is off)", false, "dumpsys", "deviceidle", "whitelist", "+"+Package)
	do("Notifications", true, "pm", "grant", Package, "android.permission.POST_NOTIFICATIONS")
	// Restart the app so it picks up the new permissions, and bring it to the
	// front: Android 13+ asks once about log access while the app is visible.
	sh("am", "force-stop", Package)
	do("Restart VoidBridge on the phone", false, "am", "start", "-n", Package+"/.MainActivity")
	return steps, nil
}
