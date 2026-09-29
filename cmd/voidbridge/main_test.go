package main

import (
	"reflect"
	"testing"
)

func TestParseSend(t *testing.T) {
	cases := []struct {
		args  []string
		dev   string
		paths []string
	}{
		{[]string{`C:\VoidBridge.exe`, "--send", "abc", `C:\a b\c.pdf`}, "abc", []string{`C:\a b\c.pdf`}},
		{[]string{"--send=abc", "x", "y"}, "abc", []string{"x", "y"}},
		{[]string{"-send", "abc"}, "abc", []string{}},
		{[]string{"--hidden"}, "", nil},
		{[]string{"--send"}, "", nil},
	}
	for _, c := range cases {
		dev, paths := parseSend(c.args)
		if dev != c.dev || (len(paths) != 0 || len(c.paths) != 0) && !reflect.DeepEqual(paths, c.paths) {
			t.Errorf("parseSend(%q) = %q %q", c.args, dev, paths)
		}
	}
}
