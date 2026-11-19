// Package kiroide holds what the proxy says about the Kiro IDE it presents
// itself as, for every package that talks to Kiro.
package kiroide

import (
	"fmt"
	"runtime"
)

// Version is the Kiro IDE version the proxy presents itself as.
const Version = "1.4.154"

// UserAgent is the User-Agent of every request to Kiro.
func UserAgent() string {
	return fmt.Sprintf("KiroIDE-%s-%s", Version, runtime.GOOS)
}
