package id

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"time"
)

func NewPublicID(now time.Time) ([]byte, error) {
	identifier := make([]byte, 16)
	milliseconds := uint64(now.UTC().UnixMilli())
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], milliseconds)
	copy(identifier[:6], encoded[2:])
	if _, err := rand.Read(identifier[6:]); err != nil {
		return nil, fmt.Errorf("generate public ID: %w", err)
	}
	return identifier, nil
}
