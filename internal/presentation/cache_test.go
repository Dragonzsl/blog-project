package presentation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

func TestRenderFlightCoalescesConcurrentMisses(t *testing.T) {
	flight := NewRenderFlight(32)
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	var renders atomic.Int32
	render := func(context.Context) ([]byte, string, error) {
		renders.Add(1)
		startOnce.Do(func() { close(started) })
		<-release
		return []byte("rendered"), "", nil
	}
	const callers = 50
	var wait sync.WaitGroup
	wait.Add(callers)
	results := make(chan string, callers)
	for index := 0; index < callers; index++ {
		go func() {
			defer wait.Done()
			body, _, _, err := flight.Do(context.Background(), "1|home", render)
			if err != nil {
				results <- err.Error()
				return
			}
			results <- string(body)
		}()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("render leader did not start")
	}
	// Wait for every follower to observe the in-flight call before releasing
	// the leader. A fixed sleep is not deterministic under the race detector.
	deadline := time.NewTimer(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	joined := false
	for !joined {
		stats := flight.Stats()
		joined = stats.Waiters == callers-1
		if joined {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			ticker.Stop()
			close(release)
			t.Fatal("render followers did not join the flight")
		}
	}
	ticker.Stop()
	if !deadline.Stop() {
		<-deadline.C
	}
	close(release)
	wait.Wait()
	close(results)
	for value := range results {
		if value != "rendered" {
			t.Fatalf("render result=%q", value)
		}
	}
	if got := renders.Load(); got != 1 {
		t.Fatalf("render calls=%d, want 1", got)
	}
	stats := flight.Stats()
	if stats.Leaders != 1 || stats.Waiters != callers-1 {
		t.Fatalf("flight stats=%+v", stats)
	}
}

func TestRenderFlightDoesNotCacheFailures(t *testing.T) {
	flight := NewRenderFlight(1)
	want := errors.New("render failed")
	var renders atomic.Int32
	render := func(context.Context) ([]byte, string, error) {
		renders.Add(1)
		return nil, "", want
	}
	for index := 0; index < 2; index++ {
		_, _, _, err := flight.Do(context.Background(), "1|broken", render)
		if !errors.Is(err, want) {
			t.Fatalf("render error=%v", err)
		}
	}
	if got := renders.Load(); got != 2 {
		t.Fatalf("failed render calls=%d, want 2", got)
	}
	if got := flight.Stats().Errors; got != 2 {
		t.Fatalf("failed render stats=%d, want 2", got)
	}
}
