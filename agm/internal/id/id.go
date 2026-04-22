package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewSessionID returns a prefixed, URL-safe random ID like "sess_a3b2c4d5e6f7".
func NewSessionID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("agm: crypto/rand failed: %v", err))
	}
	return "sess_" + hex.EncodeToString(b[:])
}
