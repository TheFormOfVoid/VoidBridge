// Package update finds new stable releases of VoidBridge on GitHub and
// downloads them, checked against the release's SHA256SUMS.txt.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Repo is the GitHub repository whose releases are checked.
const Repo = "TheFormOfVoid/VoidBridge"

// SumsFile lists the SHA-256 of every file in a release (sha256sum format).
const SumsFile = "SHA256SUMS.txt"

var (
	// APIBase is the GitHub API; tests point it elsewhere.
	APIBase = "https://api.github.com"
	Client  = &http.Client{Timeout: 10 * time.Minute}
)

// Release is the newest stable release.
type Release struct {
	Version string            // the tag, e.g. "v0.3.0"
	Page    string            // release page, for "what's new"
	Assets  map[string]string // file name -> download URL
}

func get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "VoidBridge-updater")
	resp, err := Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("can't reach GitHub: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	return resp, nil
}

// Latest returns the newest stable release. GitHub's "latest" never points
// at a pre-release or a draft, so betas are skipped.
func Latest(ctx context.Context) (*Release, error) {
	resp, err := get(ctx, APIBase+"/repos/"+Repo+"/releases/latest")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r struct {
		Tag        string `json:"tag_name"`
		URL        string `json:"html_url"`
		Prerelease bool   `json:"prerelease"`
		Draft      bool   `json:"draft"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return nil, fmt.Errorf("unexpected answer from GitHub: %w", err)
	}
	if r.Prerelease || r.Draft || r.Tag == "" {
		return nil, errors.New("no stable release found")
	}
	rel := &Release{Version: r.Tag, Page: r.URL, Assets: map[string]string{}}
	for _, a := range r.Assets {
		rel.Assets[a.Name] = a.URL
	}
	return rel, nil
}

// parse splits "v1.2.3" or "1.2.3-beta.1" into numbers and the pre-release part.
func parse(v string) (nums [3]int, pre string, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, pre, _ = strings.Cut(v, "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return nums, "", false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nums, "", false
		}
		nums[i] = n
	}
	return nums, pre, true
}

// Supported reports whether a build can update itself: release builds only,
// not "dev" or branch builds.
func Supported(current string) bool {
	_, _, ok := parse(current)
	return ok
}

// Newer reports whether latest is a newer stable version than current. A
// pre-release of the same version counts as older.
func Newer(latest, current string) bool {
	l, lpre, ok1 := parse(latest)
	c, cpre, ok2 := parse(current)
	if !ok1 || !ok2 || lpre != "" {
		return false
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return cpre != ""
}

// Fetch downloads a release file into w and checks its SHA-256 against the
// release's SHA256SUMS.txt. On error, whatever was written must be discarded.
func Fetch(ctx context.Context, rel *Release, name string, w io.Writer) error {
	url, ok := rel.Assets[name]
	if !ok {
		return fmt.Errorf("%s has no %s", rel.Version, name)
	}
	sumsURL, ok := rel.Assets[SumsFile]
	if !ok {
		return fmt.Errorf("%s has no checksums, so it can't be installed automatically", rel.Version)
	}
	resp, err := get(ctx, sumsURL)
	if err != nil {
		return err
	}
	sums, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if err != nil {
		return err
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			want = strings.ToLower(f[0])
		}
	}
	if len(want) != 64 {
		return fmt.Errorf("no checksum for %s in %s", name, rel.Version)
	}

	resp, err = get(ctx, url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, h), resp.Body); err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("the download of %s is damaged (checksum mismatch)", name)
	}
	return nil
}
