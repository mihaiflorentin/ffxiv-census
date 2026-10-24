package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
)

// uiBuildID identifies the running binary for page ETags, so a deploy
// invalidates every cached page even when the statistics snapshot is
// unchanged. It is a hash of the executable, identical across pods running
// the same image and different for any code, template or asset change. If
// the executable cannot be read, the process start time stands in: caches
// then refresh on every restart, which is safe.
var uiBuildID = sync.OnceValue(func() string {
	if path, err := os.Executable(); err == nil {
		if f, err := os.Open(path); err == nil {
			defer func() { _ = f.Close() }()
			h := sha256.New()
			if _, err := io.Copy(h, f); err == nil {
				return hex.EncodeToString(h.Sum(nil)[:8])
			}
		}
	}
	return "start-" + strconv.FormatInt(time.Now().UnixNano(), 36)
})
