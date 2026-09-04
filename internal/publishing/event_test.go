package publishing

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/zhushilin/blog-project/internal/extensions"
	"github.com/zhushilin/blog-project/internal/platform/config"
	"github.com/zhushilin/blog-project/internal/platform/database"
)

type publicationEventPlugin struct{ events int }

func (p *publicationEventPlugin) Manifest() extensions.Manifest {
	return extensions.Manifest{ID: "publication-observer", Name: "publication observer", Version: "1", APIVersion: extensions.HostAPIVersion}
}

func (p *publicationEventPlugin) Register(host *extensions.Host) error {
	return host.Subscribe("ContentPublished.v1", func(context.Context, extensions.Event) error {
		p.events++
		return nil
	})
}

func TestPublicationEventIsAtomicAndDurable(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, config.Database{Path: filepath.Join(t.TempDir(), "blog.sqlite"), BusyTimeout: config.Duration{Duration: time.Second}, CacheSizeKiB: 4096, ReadConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registry := extensions.NewRegistry(db, chi.NewRouter(), nil)
	observer := &publicationEventPlugin{}
	if err := registry.Register(observer); err != nil {
		t.Fatal(err)
	}
	if err := registry.Enable(ctx, observer.Manifest().ID); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepository(db))
	service.SetEventSink(registry)
	draft, err := service.CreateDraft(ctx, DraftInput{Title: "事件文章", Slug: "event-post", BodyMarkdown: "正文"})
	if err != nil {
		t.Fatal(err)
	}
	published, err := service.Publish(ctx, draft.ID, draft.LockVersion)
	if err != nil {
		t.Fatal(err)
	}
	if observer.events != 0 {
		t.Fatal("publication dispatched before task processing")
	}
	var eventCount, jobCount int
	if err := db.Reader.QueryRowContext(ctx, "SELECT count(*) FROM event_outbox WHERE event_name='ContentPublished.v1'").Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := db.Reader.QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE kind='core:event_dispatch'").Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 || jobCount != 1 {
		t.Fatalf("events=%d jobs=%d", eventCount, jobCount)
	}
	processed, err := registry.ProcessOne(ctx)
	if err != nil || !processed || observer.events != 1 {
		t.Fatalf("processed=%v err=%v observer=%d", processed, err, observer.events)
	}
	var eventStatus string
	if err := db.Reader.QueryRowContext(ctx, "SELECT status FROM event_outbox LIMIT 1").Scan(&eventStatus); err != nil || eventStatus != "succeeded" {
		t.Fatalf("event status=%q err=%v", eventStatus, err)
	}
	if _, err := service.Publish(ctx, published.ID, draft.LockVersion); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale publication error=%v", err)
	}
	if err := db.Reader.QueryRowContext(ctx, "SELECT count(*) FROM event_outbox").Scan(&eventCount); err != nil || eventCount != 1 {
		t.Fatalf("event count after failed transaction=%d err=%v", eventCount, err)
	}
}
