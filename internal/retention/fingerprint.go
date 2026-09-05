package retention

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// RawSource is an authoritative raw dataset snapshot. Path is part of the
// fingerprint so replacing one source with another cannot look unchanged.
type RawSource struct {
	Path string
	Data []byte
}

// Fingerprint returns a versioned, length-delimited digest of the authoritative
// dataset handle and every raw source identity/content. A nil handle is useful
// for ordinary documents, where the source is the document itself.
func Fingerprint(handle []byte, sources []RawSource) string {
	digest := sha256.New()
	digest.Write([]byte("symdesk-retention-fingerprint-v1\x00"))
	writePart := func(value []byte) {
		digest.Write([]byte(strconv.Itoa(len(value))))
		digest.Write([]byte{':'})
		digest.Write(value)
		digest.Write([]byte{'\n'})
	}
	writePart(handle)
	for _, source := range sources {
		writePart([]byte(source.Path))
		writePart(source.Data)
	}
	return hex.EncodeToString(digest.Sum(nil))
}
