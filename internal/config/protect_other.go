//go:build !windows

package config

// Without DPAPI the key is stored as-is (the file is readable only by the user).
func protect(b []byte) ([]byte, error)   { return b, nil }
func unprotect(b []byte) ([]byte, error) { return b, nil }
