//go:build !windows

package main

import "errors"

// runServiceMode is only implemented on Windows (service_windows.go).
// Keeping the stub here lets the Linux/macOS builds compile unchanged.
func runServiceMode() error {
	return errors.New("service mode is only implemented on Windows")
}
