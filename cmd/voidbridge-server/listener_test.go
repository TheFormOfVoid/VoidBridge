package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCert makes a self-signed certificate for 127.0.0.1.
func writeCert(t *testing.T, dir, cn string) (string, string) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(k)
	cp, kp := filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem")
	os.WriteFile(cp, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(kp, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	return cp, kp
}

func TestDualListener(t *testing.T) {
	dir := t.TempDir()
	cp, kp := writeCert(t, dir, "first")
	cf := &certFile{cert: cp, key: kp}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dl := newDualListener(ln, &tls.Config{GetCertificate: cf.get, MinVersion: tls.VersionTLS12})
	hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]bool{"tls": r.TLS != nil})
	})}
	go hs.Serve(dl)
	t.Cleanup(func() { hs.Close() })
	addr := ln.Addr().String()

	get := func(c *http.Client, url string) (bool, *http.Response) {
		resp, err := c.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var v map[string]bool
		json.NewDecoder(resp.Body).Decode(&v)
		return v["tls"], resp
	}
	if tlsUsed, _ := get(http.DefaultClient, "http://"+addr+"/"); tlsUsed {
		t.Fatal("plain request reported TLS")
	}
	insecure := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	tlsUsed, resp := get(insecure, "https://"+addr+"/")
	if !tlsUsed || resp.TLS.PeerCertificates[0].Subject.CommonName != "first" {
		t.Fatal("HTTPS request not served with the certificate")
	}

	// A renewed certificate is picked up without a restart.
	time.Sleep(10 * time.Millisecond)
	writeCert(t, dir, "renewed")
	future := time.Now().Add(time.Hour)
	os.Chtimes(cp, future, future)
	cf.mu.Lock()
	cf.checked = time.Time{}
	cf.mu.Unlock()
	insecure.CloseIdleConnections()
	if _, resp := get(insecure, "https://"+addr+"/"); resp.TLS.PeerCertificates[0].Subject.CommonName != "renewed" {
		t.Fatal("renewed certificate not used")
	}

	// A connection that never sends anything doesn't hold up others.
	idle, _ := net.Dial("tcp", addr)
	defer idle.Close()
	if tlsUsed, _ := get(http.DefaultClient, "http://"+addr+"/"); tlsUsed {
		t.Fatal("plain request reported TLS")
	}
}

func TestApplyOptions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "options.json")
	os.WriteFile(p, []byte(`{"port":8443,"signup":"invite","domain":" me.duckdns.org ","email":"","certfile":"fullchain.pem","keyfile":"/ssl/privkey.pem"}`), 0o600)
	listen, signup, domain, email, cert, key := ":47831", "open", "", "", "", ""
	if err := applyOptions(p, &listen, &signup, &domain, &email, &cert, &key); err != nil {
		t.Fatal(err)
	}
	if listen != ":8443" || signup != "invite" || domain != "me.duckdns.org" || cert != "/ssl/fullchain.pem" || key != "/ssl/privkey.pem" {
		t.Fatal(listen, signup, domain, cert, key)
	}
}
