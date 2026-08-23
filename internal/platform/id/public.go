package id

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
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

func EncodePublicID(identifier []byte) (string, error) {
	if len(identifier) != 16 {
		return "", fmt.Errorf("public ID must contain 16 bytes")
	}
	return hex.EncodeToString(identifier), nil
}

func DecodePublicID(value string) ([]byte, error) {
	if len(value) != 32 {
		return nil, fmt.Errorf("public ID must contain 32 hexadecimal characters")
	}
	identifier, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode public ID: %w", err)
	}
	return identifier, nil
}
