//go:build !windows

package clipboard

// System returns an in-memory clipboard on platforms without a native
// implementation, so the app can still be built and exercised for development.
func System() Clipboard { return &Memory{} }
