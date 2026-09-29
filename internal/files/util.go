package files

import (
	"context"
	"mime"
	"path/filepath"
	"strings"
)

func mimeFor(path string) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); t != "" {
		return t
	}
	return "application/octet-stream"
}

func contextFrom(done <-chan struct{}) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	if done != nil {
		go func() {
			select {
			case <-done:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, cancel
}
