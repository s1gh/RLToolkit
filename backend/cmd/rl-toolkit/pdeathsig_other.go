//go:build !linux

package main

// installParentDeathSignal is a no-op on platforms without a
// PR_SET_PDEATHSIG-equivalent. Windows handles this through a Job
// Object on the launcher side; macOS and other BSDs are not yet
// covered.
func installParentDeathSignal() {}
