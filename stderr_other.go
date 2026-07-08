//go:build !darwin

package main

import "os"

// redirectStderr is a no-op on non-darwin platforms (Windows console apps keep
// their stderr; darwin `open` detachment is the case we must handle).
func redirectStderr(f *os.File) {}
