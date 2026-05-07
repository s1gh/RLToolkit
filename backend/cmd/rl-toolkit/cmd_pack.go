package main

import (
	"flag"
	"fmt"
	"os"

	"rl-toolkit/backend/internal/pack"
)

func runPack(args []string) int {
	fs := flag.NewFlagSet("pack", flag.ContinueOnError)
	out := fs.String("out", ".", "Output directory for the .rltp file")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: rl-toolkit pack [path] [-out <dir>]\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	src := "."
	if fs.NArg() >= 1 {
		src = fs.Arg(0)
	}

	path, err := pack.Pack(src, *out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pack: %v\n", err)
		return 1
	}
	fmt.Println(path)
	return 0
}
