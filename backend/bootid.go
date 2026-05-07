package backend

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

var (
	bootIDOnce sync.Once
	bootID     string
)

// BootID returns a stable 16-char lowercase-hex identifier for this
// process. Generated once on first call from crypto/rand. Plugins use
// this to detect backend restarts (i.e. launcher restarts) and reset
// per-session state.
func BootID() string {
	bootIDOnce.Do(func() {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			// crypto/rand failure on a desktop launcher process is
			// effectively unrecoverable; surface it loudly rather than
			// invent a deterministic ID that defeats the purpose.
			panic("bootid: crypto/rand failed: " + err.Error())
		}
		bootID = hex.EncodeToString(b[:])
	})
	return bootID
}
