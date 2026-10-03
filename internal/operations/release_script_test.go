package operations

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleasePassesBuildMetadata(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "release.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, push := range []string{"0", "1"} {
		t.Run("push="+push, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "tools")
			scripts := filepath.Join(root, "scripts")
			for _, dir := range []string{bin, scripts} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, data string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(data), 0700); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(scripts, "release.sh"), string(source))
			for _, script := range []string{"generate-sbom.sh", "license-audit.sh"} {
				write(filepath.Join(scripts, script), "#!/bin/sh\nexit 0\n")
			}
			write(filepath.Join(bin, "go"), "#!/bin/sh\nexit 0\n")
			write(filepath.Join(bin, "docker"), "#!/bin/sh\nif [ \"$*\" = 'buildx version' ]; then exit 0; fi\nprintf '%s\\n' \"$@\" > \"$RELEASE_TEST_LOG\"\n")
			log := filepath.Join(root, "args")
			cmd := exec.Command("sh", filepath.Join(scripts, "release.sh"))
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "DIST_DIR="+filepath.Join(root, "dist"), "VERSION=0.1.0-alpha.1", "COMMIT=abc123", "BUILD_TIME=2026-10-03T00:00:00Z", "PUSH="+push, "RELEASE_TEST_LOG="+log)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("release failed: %v: %s", err, output)
			}
			args, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			for _, arg := range []string{"VERSION=0.1.0-alpha.1", "COMMIT=abc123", "BUILD_TIME=2026-10-03T00:00:00Z"} {
				if !strings.Contains(string(args), "--build-arg\n"+arg+"\n") {
					t.Fatalf("missing metadata %q in %s", arg, args)
				}
			}
			if strings.Contains(string(args), "--push\n") != (push == "1") {
				t.Fatalf("incorrect push behavior: %s", args)
			}
			if strings.Contains(string(args), "--output\n") != (push == "0") {
				t.Fatalf("incorrect archive behavior: %s", args)
			}
		})
	}
}
