package presentation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPageCacheMemoryDiskAndEpochIsolation(t *testing.T) {
	directory := t.TempDir()
	cache, err := NewPageCache(directory, 1, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	entry := CacheEntry{Key: "article:first", Epoch: 2, Status: 200, ContentType: "text/html", ETag: `"etag"`, Body: []byte("first")}
	if err := cache.Put(entry); err != nil {
		t.Fatal(err)
	}
	got, ok := cache.Get(entry.Key, 2)
	if !ok || string(got.Body) != "first" {
		t.Fatalf("memory cache = %+v, %v", got, ok)
	}
	got.Body[0] = 'X'
	again, _ := cache.Get(entry.Key, 2)
	if string(again.Body) != "first" {
		t.Fatal("cache body was mutated by caller")
	}
	if _, ok := cache.Get(entry.Key, 3); ok {
		t.Fatal("old epoch cache entry matched new epoch")
	}

	reopened, err := NewPageCache(directory, 1, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	fromDisk, ok := reopened.Get(entry.Key, 2)
	if !ok || string(fromDisk.Body) != "first" {
		t.Fatalf("disk cache = %+v, %v", fromDisk, ok)
	}
}

func TestPageCachePrunesOldEpochs(t *testing.T) {
	directory := t.TempDir()
	cache, err := NewPageCache(directory, 2, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	for epoch := int64(1); epoch <= 3; epoch++ {
		if err := cache.Put(CacheEntry{Key: "home", Epoch: epoch, Status: 200, Body: []byte("page")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := cache.Prune(3); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(directory, "epoch-*.cache"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		contents, _ := os.ReadDir(directory)
		t.Fatalf("cache files after prune = %d (%v)", len(matches), contents)
	}
}
