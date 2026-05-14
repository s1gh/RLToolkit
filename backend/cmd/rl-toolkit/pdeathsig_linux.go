//go:build linux

package main

import (
	"log"

	"golang.org/x/sys/unix"
)

// installParentDeathSignal asks the kernel to send this process
// SIGTERM the moment its parent goes away. The launcher (rl-widget)
// spawns rl-toolkit as a sidecar; if the launcher is killed by
// anything other than a clean exit (Task Manager equivalent, panic,
// OOM, kernel kill), the sidecar would otherwise be reparented to
// PID 1 and run forever. The existing SIGTERM handler in awaitSignal
// gives us a graceful shutdown when the kernel delivers the signal.
//
// Linux-only because PR_SET_PDEATHSIG has no portable equivalent.
// macOS and other BSDs need a polling-based approach which we don't
// ship today; Windows is handled by a Job Object on the launcher
// side, not in the backend.
func installParentDeathSignal() {
	if err := unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGTERM), 0, 0, 0); err != nil {
		log.Printf("[server] PR_SET_PDEATHSIG: %v", err)
	}
}
