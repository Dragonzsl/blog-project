package slug

import "testing"

func TestNormalizeUnicodeSlug(t *testing.T) {
	display, key, err := Normalize("  Café-随笔  ")
	if err != nil {
		t.Fatal(err)
	}
	if display != "Café-随笔" || key != "café-随笔" {
		t.Fatalf("display=%q key=%q", display, key)
	}
	for _, invalid := range []string{"", "-bad", "bad-", "bad--slug", "bad/path", "bad slug"} {
		if _, _, err := Normalize(invalid); err == nil {
			t.Fatalf("invalid slug %q was accepted", invalid)
		}
	}
}
