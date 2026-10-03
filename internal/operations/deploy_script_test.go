package operations

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeployRequiresPublicReadinessAndExplicitComposeFile(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "deploy.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, address, readyStatus, homeStatus string
		wantSuccess, wantInsecure              bool
	}{
		{"local certificate", "https://localhost", "200", "200", true, true},
		{"production certificate", "https://blog.example.test", "200", "200", true, false},
		{"public readiness unavailable", "https://localhost", "503", "200", false, true},
		{"homepage unavailable", "https://localhost", "200", "502", false, true},
		{"production TLS failure", "https://blog.example.test", "000", "000", false, false},
		{"redirect is not ready", "https://localhost", "302", "200", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "tools")
			if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			write := func(path, content string, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(path, []byte(content), mode); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(root, "scripts", "deploy.sh"), string(source), 0o700)
			write(filepath.Join(root, ".env"), "BLOG_SITE_ADDRESS="+tc.address+"\n", 0o600)
			write(filepath.Join(root, "compose.override.yaml"), "invalid local override\n", 0o600)
			write(filepath.Join(bin, "docker"), `#!/bin/sh
printf '%s\n' "$*" >> "$DEPLOY_TEST_LOG"
if [ "$*" = 'compose version' ]; then exit 0; fi
[ "$1" = compose ] && [ "$2" = --env-file ] && [ "$4" = -f ] || exit 8
[ "$5" = "$DEPLOY_TEST_ROOT/compose.yaml" ] || exit 9
shift 5
case "$*" in
  'exec -T caddy printenv BLOG_SITE_ADDRESS') printf '%s\n' "$DEPLOY_TEST_ADDRESS" ;;
esac
`, 0o700)
			write(filepath.Join(bin, "curl"), `#!/bin/sh
printf 'curl %s\n' "$*" >> "$DEPLOY_TEST_LOG"
for arg do url=$arg; done
case "$url" in
  */readyz) status=$DEPLOY_TEST_READY_STATUS ;;
  */) status=$DEPLOY_TEST_HOME_STATUS ;;
  *) exit 7 ;;
esac
printf '%s' "$status"
[ "$status" != 000 ]
`, 0o700)
			logPath := filepath.Join(root, "commands.log")
			command := exec.Command("sh", filepath.Join(root, "scripts", "deploy.sh"), "--no-build", "--no-backup", "--wait", "1")
			command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"COMPOSE_FILE=/unwanted-compose.yaml", "DEPLOY_ENV_FILE="+filepath.Join(root, ".env"),
				"DEPLOY_TEST_ROOT="+root, "DEPLOY_TEST_LOG="+logPath, "DEPLOY_TEST_ADDRESS="+tc.address,
				"DEPLOY_TEST_READY_STATUS="+tc.readyStatus, "DEPLOY_TEST_HOME_STATUS="+tc.homeStatus)
			output, err := command.CombinedOutput()
			if (err == nil) != tc.wantSuccess {
				t.Fatalf("success=%t, want %t: %s", err == nil, tc.wantSuccess, output)
			}
			commands, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(output), "部署完成") != tc.wantSuccess {
				t.Fatalf("incorrect deployment result: %s", output)
			}
			if strings.Contains(string(commands), "--insecure") != tc.wantInsecure {
				t.Fatalf("unexpected TLS verification options: %s", commands)
			}
			if tc.wantSuccess && !strings.Contains(string(commands), tc.address+"/\n") {
				t.Fatal("deployment did not check the homepage")
			}
		})
	}
}
