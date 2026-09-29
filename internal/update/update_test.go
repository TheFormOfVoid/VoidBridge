package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v0.3.0", "v0.2.1", true},
		{"v0.3.0", "v0.3.0", false},
		{"v0.2.9", "v0.3.0", false},
		{"v1.0.0", "v0.99.99", true},
		{"v0.10.0", "v0.9.0", true},
		{"v0.3.0", "v0.3.0-beta.2", true},
		{"v0.3.1-beta.1", "v0.3.0", false}, // betas are never offered
		{"v0.3.0", "dev", false},
		{"v0.3.0", "main", false},
		{"v0.3.0", "dev-42", false},
		{"garbage", "v0.1.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.latest, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.latest, c.current, got)
		}
	}
	if Supported("dev") || !Supported("v0.3.0") || !Supported("0.3.0") {
		t.Error("Supported")
	}
}

func fakeGitHub(t *testing.T, exe []byte, sums string) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + Repo + "/releases/latest":
			assets := fmt.Sprintf(`[{"name":"app.exe","browser_download_url":"%s/dl/app.exe"}`, srv.URL)
			if sums != "" {
				assets += fmt.Sprintf(`,{"name":"SHA256SUMS.txt","browser_download_url":"%s/dl/sums"}`, srv.URL)
			}
			fmt.Fprintf(w, `{"tag_name":"v9.0.0","html_url":"https://example/rel","assets":%s]}`, assets)
		case "/dl/app.exe":
			w.Write(exe)
		case "/dl/sums":
			w.Write([]byte(sums))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := APIBase
	APIBase = srv.URL
	t.Cleanup(func() { APIBase = old })
}

func TestFetch(t *testing.T) {
	exe := bytes.Repeat([]byte("MZ new version "), 1000)
	h := sha256.Sum256(exe)
	good := hex.EncodeToString(h[:])
	ctx := context.Background()

	fakeGitHub(t, exe, good+"  other.apk\n"+good+"  app.exe\n")
	rel, err := Latest(ctx)
	if err != nil || rel.Version != "v9.0.0" || rel.Page != "https://example/rel" {
		t.Fatal(rel, err)
	}
	var buf bytes.Buffer
	if err := Fetch(ctx, rel, "app.exe", &buf); err != nil || !bytes.Equal(buf.Bytes(), exe) {
		t.Fatal(err)
	}
	if err := Fetch(ctx, rel, "missing.exe", &buf); err == nil {
		t.Fatal("fetched a file the release doesn't have")
	}

	fakeGitHub(t, exe, strings.Repeat("0", 64)+"  app.exe\n")
	rel, _ = Latest(ctx)
	if err := Fetch(ctx, rel, "app.exe", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("bad checksum accepted: %v", err)
	}

	fakeGitHub(t, exe, "")
	rel, _ = Latest(ctx)
	if err := Fetch(ctx, rel, "app.exe", &bytes.Buffer{}); err == nil {
		t.Fatal("release without checksums accepted")
	}
}
