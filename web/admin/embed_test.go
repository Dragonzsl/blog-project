package admin

import (
	"io/fs"
	"strings"
	"testing"
)

func TestAdminShellSidebarContract(t *testing.T) {
	shell, err := fs.ReadFile(Files, "templates/admin_shell.html")
	if err != nil {
		t.Fatal(err)
	}
	markup := string(shell)
	for _, expected := range []string{
		`id="admin-sidebar"`,
		`data-drawer-name="admin"`,
		`data-sidebar-toggle`,
		`data-sidebar-scrim="admin"`,
		`aria-label="站主管理"`,
		`aria-current="page"`,
		`admin_nav_icon`,
	} {
		if !strings.Contains(markup, expected) {
			t.Fatalf("admin shell missing sidebar contract %q", expected)
		}
	}
	if strings.Contains(markup, "admin-mobile-nav") || strings.Contains(markup, "<details") {
		t.Fatal("admin shell still uses the legacy mobile details navigation")
	}

	styles, err := fs.ReadFile(Files, "static/admin.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(styles)
	for _, expected := range []string{"@media (max-width: 1023px)", ".admin-sidebar[aria-hidden=\"false\"]", "html:not(.js) .admin-sidebar"} {
		if !strings.Contains(css, expected) {
			t.Fatalf("admin stylesheet missing responsive sidebar contract %q", expected)
		}
	}

	script, err := fs.ReadFile(Files, "static/admin.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, expected := range []string{"setupSidebars", "event.key === \"Escape\"", "requestAnimationFrame"} {
		if !strings.Contains(js, expected) {
			t.Fatalf("admin script missing sidebar behavior %q", expected)
		}
	}
}
