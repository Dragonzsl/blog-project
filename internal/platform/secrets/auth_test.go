package secrets

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zhushilin/blog-project/internal/platform/config"
)

func TestLoadOrCreateAuthSecretPersistsSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "auth.key")
	cfg := config.Security{AuthSecretFile: path}
	first, err := LoadOrCreateAuthSecret(cfg)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	second, err := LoadOrCreateAuthSecret(cfg)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("persisted secret changed")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secret permissions = %o", info.Mode().Perm())
	}
}
