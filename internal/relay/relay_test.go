package relay

import (
	"errors"
	"testing"
)

func TestPlainHTTPOnlyAtHome(t *testing.T) {
	// 8.8.8.8 stands in for any internet address; the dial is refused before
	// anything is sent.
	api := &API{Base: "http://8.8.8.8:47831"}
	if _, err := api.Info(); !errors.Is(err, ErrInsecure) {
		t.Fatalf("plain HTTP to the internet: %v", err)
	}
	if _, err := (&API{Base: "http://127.0.0.1:1"}).Info(); errors.Is(err, ErrInsecure) {
		t.Fatal("refused a home address")
	}
}
