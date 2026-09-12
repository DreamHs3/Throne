package main

// PC-100 run modes. The legacy mode is the historical GUI-parent child
// process: no arguments, socket name via THRONE_CORE_SOCKET, parent identity
// enforced by parentcheck. The service mode is an explicit, separate entry
// point (see service_windows.go / service_other.go).

type runMode int

const (
	runModeLegacy runMode = iota
	runModeService
	runModeUnknown
)

func runModeFromArgs(args []string) runMode {
	switch {
	case len(args) == 0:
		return runModeLegacy
	case len(args) == 1 && args[0] == "service":
		return runModeService
	default:
		return runModeUnknown
	}
}
