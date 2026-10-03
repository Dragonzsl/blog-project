package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhushilin/blog-project/internal/platform/config"
)

const authSecretBytes = 32

func LoadOrCreateAuthSecret(cfg config.Security) ([]byte, error) {
	if value := strings.TrimSpace(cfg.AuthSecret); value != "" {
		if len(value) < authSecretBytes {
			return nil, fmt.Errorf("BLOG_AUTH_SECRET must contain at least %d bytes", authSecretBytes)
		}
		return []byte(value), nil
	}

	contents, err := os.ReadFile(cfg.AuthSecretFile)
	if err == nil {
		info, statErr := os.Stat(cfg.AuthSecretFile)
		if statErr != nil {
			return nil, fmt.Errorf("inspect auth secret file: %w", statErr)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("auth secret file permissions must not grant group or other access")
		}
		secret, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(contents)))
		if err != nil {
			return nil, fmt.Errorf("decode auth secret file: %w", err)
		}
		if len(secret) != authSecretBytes {
			return nil, fmt.Errorf("auth secret file must decode to %d bytes", authSecretBytes)
		}
		return secret, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read auth secret file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.AuthSecretFile), 0o700); err != nil {
		return nil, fmt.Errorf("create auth secret directory: %w", err)
	}
	secret := make([]byte, authSecretBytes)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate auth secret: %w", err)
	}
	encoded := []byte(base64.RawURLEncoding.EncodeToString(secret) + "\n")
	file, err := os.OpenFile(cfg.AuthSecretFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return LoadOrCreateAuthSecret(cfg)
	}
	if err != nil {
		return nil, fmt.Errorf("create auth secret file: %w", err)
	}
	if _, err := file.Write(encoded); err != nil {
		file.Close()
		return nil, fmt.Errorf("write auth secret file: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return nil, fmt.Errorf("sync auth secret file: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close auth secret file: %w", err)
	}
	return secret, nil
}
