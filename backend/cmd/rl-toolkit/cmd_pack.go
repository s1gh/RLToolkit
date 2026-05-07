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
	flagArgs, positional := splitFlagsAndPositional(args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	src := "."
	if len(positional) >= 1 {
		src = positional[0]
	}

	path, err := pack.Pack(src, *out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pack: %v\n", err)
		return 1
	}
	fmt.Println(path)
	return 0
}
