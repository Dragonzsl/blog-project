package presentation

import (
	"bytes"
	"testing"
)

func TestSearchRenderCacheIsBoundedAndEpochScoped(t *testing.T) {
	cache := newSearchRenderCache()
	body := []byte("rendered search page")
	cache.Put("stable", 7, body)
	body[0] = 'X'
	hit, ok := cache.Get("stable", 7)
	if !ok || !bytes.Equal(hit, []byte("rendered search page")) {
		t.Fatalf("search render cache hit = %q, ok=%v", hit, ok)
	}
	hit[0] = 'Y'
	hit, ok = cache.Get("stable", 7)
	if !ok || !bytes.Equal(hit, []byte("rendered search page")) {
		t.Fatalf("search render cache returned mutable storage = %q, ok=%v", hit, ok)
	}
	if _, ok := cache.Get("stable", 8); ok {
		t.Fatal("search render cache crossed render epochs")
	}

	for index := 0; index < searchRenderCacheMaxEntries+8; index++ {
		cache.Put(string(rune('a'+index)), 7, []byte("small body"))
	}
	cache.mu.RLock()
	entries, size := cache.recent.Len(), cache.memoryBytes
	cache.mu.RUnlock()
	if entries > searchRenderCacheMaxEntries || size > searchRenderCacheMaxBytes {
		t.Fatalf("search render cache exceeded bound: entries=%d bytes=%d", entries, size)
	}
}
