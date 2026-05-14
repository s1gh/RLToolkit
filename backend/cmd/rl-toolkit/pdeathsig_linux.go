//go:build linux

package main

import (
	"log"
	"os"
	"syscall"
	"time"
)

// installParentDeathSignal starts a poll loop that watches getppid().
// When the parent goes away (getppid returns 1, meaning we've been
// reparented to init), send ourselves SIGTERM so the existing
// awaitSignal handler runs a clean shutdown.
//
// We don't use prctl(PR_SET_PDEATHSIG) here even though the kernel
// API exists. PR_SET_PDEATHSIG binds to the calling THREAD's parent,
// not the process parent. The Go runtime spawns and retires worker
// threads on its own schedule; the thread that called prctl can be
// torn down at any moment after init, and the kernel then fires the
// death signal even though our actual parent process is alive. The
// symptom is a backend that exits a second or two after spawn, which
// isn't what we want.
//
// Polling getppid is cheap (one syscall per second) and side-steps
// the thread-affinity trap entirely.
//
// No-op when the parent is already PID 1 at startup (standalone run
// from a shell that's been daemonized away, or launched directly by
// systemd / openrc / similar). We don't want to self-terminate on
// boot just because nobody adopted us.
func installParentDeathSignal() {
	startupPpid := os.Getppid()
	if startupPpid == 1 {
		return
	}
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for range t.C {
			if os.Getppid() == 1 {
				log.Printf("[server] parent process gone; shutting down")
				_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
				return
			}
		}
	}()
}
