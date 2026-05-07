// Command rl-toolkit is the binary entry point. The actual logic
// lives in the backend package (and its forthcoming subpackages),
// keeping this file a thin shim so cmd-level concerns stay separate
// from library code.
package main

import "rl-toolkit/backend"

func main() {
	backend.Main()
}
