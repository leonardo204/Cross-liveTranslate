//go:build !darwin && !windows

package tray

// Init is a no-op on platforms without a tray backend.
func Init(h Handlers) error {
	handlers = h
	return nil
}

// SetStatus is a no-op stub.
func SetStatus(string) {}

// SetRunning is a no-op stub.
func SetRunning(bool) {}

// SetHUDVisible is a no-op stub.
func SetHUDVisible(bool) {}
