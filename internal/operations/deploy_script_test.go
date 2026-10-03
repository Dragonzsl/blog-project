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
if [ "$*" = info ]; then exit 0; fi
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

func TestDeployConfiguresDomainWithoutExecutingOrReplacingOtherSettings(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "deploy.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, existing, input, wantAddress string
		args                               []string
		wantFailure, daemonUnavailable     bool
	}{
		{name: "fresh domain", args: []string{"--domain", "Blog.Example.COM"}, wantAddress: "https://blog.example.com"},
		{name: "HTTPS URL", args: []string{"--domain=https://blog.example.com/"}, wantAddress: "https://blog.example.com"},
		{name: "prompt", args: []string{"--configure"}, input: "blog.example.com\n", wantAddress: "https://blog.example.com"},
		{name: "prompt retries invalid domain", args: []string{"--configure"}, input: "http://bad.example\nblog.example.com\n", wantAddress: "https://blog.example.com"},
		{name: "prompt local default", args: []string{"--configure"}, input: "\n", wantAddress: "https://localhost"},
		{name: "automation local default", args: []string{"--non-interactive"}, wantAddress: "https://localhost"},
		{name: "existing unchanged", existing: "BLOG_SITE_ADDRESS=https://existing.example.com\n", wantAddress: "https://existing.example.com"},
		{name: "reconfigure", existing: "BLOG_SITE_ADDRESS=https://old.example.com\n", args: []string{"--domain", "new.example.com"}, wantAddress: "https://new.example.com"},
		{name: "append missing address", existing: "# local settings\n", args: []string{"--domain", "new.example.com"}, wantAddress: "https://new.example.com"},
		{name: "remove duplicate addresses", existing: "BLOG_SITE_ADDRESS=https://old.example.com\nBLOG_SITE_ADDRESS=https://other.example.com\n", args: []string{"--domain", "new.example.com"}, wantAddress: "https://new.example.com"},
		{name: "invalid reconfigure preserves file", existing: "BLOG_SITE_ADDRESS=https://old.example.com\n", args: []string{"--domain", "bad/path"}, wantFailure: true},
		{name: "reject URL path", args: []string{"--domain", "example.com/path"}, wantFailure: true},
		{name: "reject HTTP", args: []string{"--domain", "http://example.com"}, wantFailure: true},
		{name: "reject wildcard", args: []string{"--domain", "*.example.com"}, wantFailure: true},
		{name: "reject address injection", args: []string{"--domain", "example.com\nBLOG_COOKIE_SECURE=false"}, wantFailure: true},
		{name: "reject credentials", args: []string{"--domain", "user:password@example.com"}, wantFailure: true},
		{name: "reject IP", args: []string{"--domain", "127.0.0.1"}, wantFailure: true},
		{name: "reject local domain", args: []string{"--domain", "blog.local"}, wantFailure: true},
		{name: "reject malformed label", args: []string{"--domain", "-blog.example.com"}, wantFailure: true},
		{name: "cancelled prompt", args: []string{"--configure"}, wantFailure: true},
		{name: "automation reconfigure needs domain", args: []string{"--configure", "--non-interactive"}, wantFailure: true},
		{name: "daemon unavailable", args: []string{"--domain", "example.com"}, wantFailure: true, daemonUnavailable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "tools")
			for _, dir := range []string{bin, filepath.Join(root, "scripts")} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, content string, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(path, []byte(content), mode); err != nil {
					t.Fatal(err)
				}
			}
			marker := filepath.Join(root, "must-not-execute")
			privateSetting := "BLOG_AUTH_SECRET=$(touch " + marker + ")\nBLOG_LOG_LEVEL=debug\n"
			envPath := filepath.Join(root, ".env")
			write(filepath.Join(root, "scripts", "deploy.sh"), string(source), 0o700)
			write(filepath.Join(root, ".env.example"), "BLOG_SITE_ADDRESS=https://localhost\n"+privateSetting, 0o600)
			if tc.existing != "" {
				write(envPath, tc.existing+privateSetting, 0o600)
			}
			write(filepath.Join(bin, "docker"), `#!/bin/sh
if [ "$*" = info ]; then [ "$DEPLOY_TEST_DAEMON" = available ]; exit; fi
if [ "$*" = 'compose version' ]; then exit 0; fi
[ "$1" = compose ] && [ "$2" = --env-file ] && [ "$4" = -f ] || exit 8
envfile=$3
shift 5
printf '%s\n' "$*" >> "$DEPLOY_TEST_LOG"
case "$*" in
  'exec -T caddy printenv BLOG_SITE_ADDRESS')
    address=$(sed -n 's/^BLOG_SITE_ADDRESS=//p' "$envfile")
    if [ "$DEPLOY_TEST_CONFIGURE" = yes ]; then [ "$BLOG_SITE_ADDRESS" = "$address" ] || exit 11; fi
    printf '%s\n' "$address" ;;
esac
`, 0o700)
			write(filepath.Join(bin, "curl"), "#!/bin/sh\nprintf 200\n", 0o700)
			logPath := filepath.Join(root, "commands.log")
			args := append([]string{filepath.Join(root, "scripts", "deploy.sh"), "--no-build", "--no-backup", "--wait", "1"}, tc.args...)
			command := exec.Command("sh", args...)
			command.Stdin = strings.NewReader(tc.input)
			daemon := "available"
			if tc.daemonUnavailable {
				daemon = "unavailable"
			}
			configure := "no"
			if tc.existing == "" || len(tc.args) > 0 {
				configure = "yes"
			}
			command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"DEPLOY_TEST_CONFIGURE="+configure, "DEPLOY_ENV_FILE="+envPath, "DEPLOY_DOMAIN=", "DEPLOY_TEST_LOG="+logPath, "DEPLOY_TEST_DAEMON="+daemon,
				"BLOG_SITE_ADDRESS=https://stale-shell.example.com")
			output, err := command.CombinedOutput()
			if (err != nil) != tc.wantFailure {
				t.Fatalf("failure=%t, want %t: %s", err != nil, tc.wantFailure, output)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("environment file was executed")
			}
			if tc.wantFailure {
				if _, err := os.Stat(logPath); !os.IsNotExist(err) {
					t.Fatal("invalid input reached Compose deployment")
				}
				if tc.existing == "" {
					if _, err := os.Stat(envPath); !os.IsNotExist(err) {
						t.Fatal("failed configuration created an environment file")
					}
				} else if saved, err := os.ReadFile(envPath); err != nil || string(saved) != tc.existing+privateSetting {
					t.Fatal("failed reconfiguration changed the existing environment file")
				}
				return
			}
			contents, err := os.ReadFile(envPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(contents), "BLOG_SITE_ADDRESS="+tc.wantAddress+"\n") || strings.Count(string(contents), "BLOG_SITE_ADDRESS=") != 1 {
				t.Fatal("incorrect or duplicate saved site address")
			}
			if !strings.Contains(string(contents), privateSetting) {
				t.Fatal("deployment replaced unrelated configuration")
			}
			if strings.Contains(string(output), "BLOG_AUTH_SECRET") {
				t.Fatal("deployment printed a private setting")
			}
			info, err := os.Stat(envPath)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatal("environment file must remain private")
			}
			if !strings.Contains(string(output), tc.wantAddress+"/admin/setup") {
				t.Fatal("missing initialization URL")
			}
		})
	}
}
