//go:build ctyune2e

package providers

import "os"

func osGetenv(k string) string { return os.Getenv(k) }
