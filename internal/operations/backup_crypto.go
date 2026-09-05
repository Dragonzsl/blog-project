package operations

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
)

const (
	backupCipherMagic = "BLOG-BACKUP-AESGCM-1\n"
	backupCipherChunk = 1 << 20
)

type BackupCipher interface {
	Name() string
	Encrypt(context.Context, io.Reader, io.Writer) error
	Decrypt(context.Context, io.Reader, io.Writer) error
}

type AESGCMBackupCipher struct{ aead cipher.AEAD }

func NewAESGCMBackupCipher(key []byte) (*AESGCMBackupCipher, error) {
	if len(key) != 32 {
		return nil, errors.New("backup encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &AESGCMBackupCipher{aead: aead}, nil
}

func LoadAESGCMBackupCipher(path string) (*AESGCMBackupCipher, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("backup encryption key file is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("backup encryption key file must be a private regular file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(contents))
	if len(trimmed) == 64 {
		decoded, decodeErr := hex.DecodeString(trimmed)
		if decodeErr != nil {
			return nil, errors.New("backup encryption key must be raw 32 bytes or 64 hex characters")
		}
		contents = decoded
	} else if len(contents) != 32 {
		contents = []byte(trimmed)
	}
	return NewAESGCMBackupCipher(contents)
}

func (c *AESGCMBackupCipher) Name() string { return "aes-gcm-256" }

func (c *AESGCMBackupCipher) Encrypt(ctx context.Context, source io.Reader, destination io.Writer) error {
	if _, err := io.WriteString(destination, backupCipherMagic); err != nil {
		return err
	}
	prefix := make([]byte, 8)
	if _, err := rand.Read(prefix); err != nil {
		return err
	}
	if _, err := destination.Write(prefix); err != nil {
		return err
	}
	buffer := make([]byte, backupCipherChunk)
	var index uint32
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := io.ReadFull(source, buffer)
		if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if count > 0 {
			nonce := backupNonce(prefix, index, c.aead.NonceSize())
			sealed := c.aead.Seal(nil, nonce, buffer[:count], nil)
			if len(sealed) > int(^uint32(0)) {
				return errors.New("encrypted backup chunk is too large")
			}
			var length [4]byte
			binary.BigEndian.PutUint32(length[:], uint32(len(sealed)))
			if _, err := destination.Write(length[:]); err != nil {
				return err
			}
			if _, err := destination.Write(sealed); err != nil {
				return err
			}
			index++
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			return nil
		}
	}
}

func (c *AESGCMBackupCipher) Decrypt(ctx context.Context, source io.Reader, destination io.Writer) error {
	magic := make([]byte, len(backupCipherMagic))
	if _, err := io.ReadFull(source, magic); err != nil {
		return err
	}
	if string(magic) != backupCipherMagic {
		return errors.New("backup encryption header is invalid")
	}
	prefix := make([]byte, 8)
	if _, err := io.ReadFull(source, prefix); err != nil {
		return err
	}
	var index uint32
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var length [4]byte
		_, err := io.ReadFull(source, length[:])
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return errors.New("encrypted backup frame is truncated")
		}
		size := binary.BigEndian.Uint32(length[:])
		if size < uint32(c.aead.Overhead()) || size > backupCipherChunk+uint32(c.aead.Overhead()) {
			return errors.New("encrypted backup frame size is invalid")
		}
		sealed := make([]byte, int(size))
		if _, err := io.ReadFull(source, sealed); err != nil {
			return errors.New("encrypted backup frame is truncated")
		}
		nonce := backupNonce(prefix, index, c.aead.NonceSize())
		plain, err := c.aead.Open(nil, nonce, sealed, nil)
		if err != nil {
			return errors.New("encrypted backup authentication failed")
		}
		if _, err := destination.Write(plain); err != nil {
			return err
		}
		index++
	}
}

func backupNonce(prefix []byte, index uint32, size int) []byte {
	nonce := make([]byte, size)
	copy(nonce, prefix)
	binary.BigEndian.PutUint32(nonce[len(nonce)-4:], index)
	return nonce
}
