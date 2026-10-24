package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
)

// base64URL encodes a logger expression the way Bee's /loggers route
// expects it (decBase64url).
func base64URL(s string) string { return base64.URLEncoding.EncodeToString([]byte(s)) }

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// readLimited reads at most n bytes of the request body.
func readLimited(w http.ResponseWriter, r *http.Request, n int64) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(w, r.Body, n))
}
