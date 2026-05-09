package bootid

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

var (
	once sync.Once
	id   string
)

// Get returns a stable 16-char lowercase-hex identifier for this
// process. Generated once on first call from crypto/rand. Plugins use
// this to detect backend restarts (i.e. launcher restarts) and reset
// per-session state.
func Get() string {
	once.Do(func() {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			// Falling back to a deterministic ID would defeat the
			// purpose, so fail loudly instead.
			panic("bootid: crypto/rand failed: " + err.Error())
		}
		id = hex.EncodeToString(b[:])
	})
	return id
}
