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

func TestFromTitle(t *testing.T) {
	for _, test := range []struct {
		title, fallback, want string
	}{
		{title: "测试文章 01：重新开始", fallback: "article", want: "测试文章-01-重新开始"},
		{title: "  A calm note / v2  ", fallback: "article", want: "a-calm-note-v2"},
		{title: "🎈", fallback: "article", want: "article"},
	} {
		display, key, err := FromTitle(test.title, test.fallback)
		if err != nil {
			t.Fatalf("FromTitle(%q): %v", test.title, err)
		}
		if display != test.want || key != test.want {
			t.Fatalf("FromTitle(%q) = display %q key %q, want %q", test.title, display, key, test.want)
		}
	}
}

func TestAppendSuffix(t *testing.T) {
	display, key, err := AppendSuffix("测试文章", 2)
	if err != nil {
		t.Fatal(err)
	}
	if display != "测试文章-2" || key != "测试文章-2" {
		t.Fatalf("AppendSuffix = display %q key %q", display, key)
	}
}
