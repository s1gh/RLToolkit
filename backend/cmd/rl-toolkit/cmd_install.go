package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"rl-toolkit/backend/internal/install"
)

func runInstall(args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	pluginDir := fs.String("plugins", filepath.Join(exeDirOrDot(), "plugins"), "Plugin directory")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: rl-toolkit install <file.rltp> [-plugins <dir>]\n\n")
		fs.PrintDefaults()
	}
	flagArgs, positional := splitFlagsAndPositional(args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(positional) < 1 {
		fs.Usage()
		return 2
	}
	rltp := positional[0]

	name, err := install.Install(rltp, *pluginDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "install: %v\n", err)
		return 1
	}
	fmt.Printf("Installed %s into %s\n", name, *pluginDir)
	return 0
}

func runUninstall(args []string) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	pluginDir := fs.String("plugins", filepath.Join(exeDirOrDot(), "plugins"), "Plugin directory")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: rl-toolkit uninstall <name> [-plugins <dir>]\n\n")
		fs.PrintDefaults()
	}
	flagArgs, positional := splitFlagsAndPositional(args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(positional) < 1 {
		fs.Usage()
		return 2
	}
	name := positional[0]
	if err := install.Uninstall(name, *pluginDir); err != nil {
		fmt.Fprintf(os.Stderr, "uninstall: %v\n", err)
		return 1
	}
	fmt.Printf("Uninstalled %s\n", name)
	return 0
}

func exeDirOrDot() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}
