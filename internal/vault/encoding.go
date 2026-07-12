package vault

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// decodeB64 decodes a libsodium ORIGINAL (standard, padded) base64 string.
func decodeB64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// decodeHexNoSpaces decodes a hex recovery code, ignoring the grouping spaces
// the web shows for readability.
func decodeHexNoSpaces(s string) ([]byte, error) {
	return hex.DecodeString(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
}
