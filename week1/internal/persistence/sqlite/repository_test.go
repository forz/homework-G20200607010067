package sqlite_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/homework-G20200607010067/week1/internal/domain"
	gatewaysqlite "github.com/homework-G20200607010067/week1/internal/persistence/sqlite"
)

func TestPromptRepositoryCreate(t *testing.T) {
	database := openTestDatabase(t)
	repository := gatewaysqlite.NewPromptRepository(database, 100)
	ctx := context.Background()

	first, err := repository.Create(ctx, domain.PromptCreate{ID: "extractor", Name: "Extractor", Role: domain.RoleSystem, Content: "v1"})
	if err != nil || first.Version != 1 || !first.IsActive {
		t.Fatalf("first Create()=%#v err=%v", first, err)
	}
	second, err := repository.Create(ctx, domain.PromptCreate{ID: "extractor", Name: "Extractor", Role: domain.RoleSystem, Content: "v2"})
	if err != nil || second.Version != 2 || second.IsActive {
		t.Fatalf("second Create()=%#v err=%v", second, err)
	}
	explicit, err := repository.Get(ctx, "extractor", &second.Version)
	if err != nil || explicit.Content != "v2" {
		t.Fatalf("explicit Get()=%#v err=%v", explicit, err)
	}
	active, err := repository.Get(ctx, "extractor", nil)
	if err != nil || active.Version != 1 {
		t.Fatalf("active Get()=%#v err=%v", active, err)
	}

	const concurrent = 8
	versions := make(chan int, concurrent)
	errorsSeen := make(chan error, concurrent)
	var group sync.WaitGroup
	for index := 0; index < concurrent; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			created, createErr := repository.Create(ctx, domain.PromptCreate{
				ID: "extractor", Name: "Extractor", Role: domain.RoleSystem, Content: fmt.Sprintf("concurrent-%d", index),
			})
			if createErr != nil {
				errorsSeen <- createErr
				return
			}
			versions <- created.Version
		}(index)
	}
	group.Wait()
	close(errorsSeen)
	close(versions)
	for createErr := range errorsSeen {
		t.Fatalf("concurrent Create() error=%v", createErr)
	}
	gotVersions := make([]int, 0, concurrent)
	for version := range versions {
		gotVersions = append(gotVersions, version)
	}
	sort.Ints(gotVersions)
	for index, version := range gotVersions {
		if want := index + 3; version != want {
			t.Fatalf("concurrent versions=%v, want contiguous range starting at 3", gotVersions)
		}
	}
	items, err := repository.List(ctx, "extractor", 100)
	if err != nil || len(items) != concurrent+2 {
		t.Fatalf("List() count=%d err=%v", len(items), err)
	}
}

func TestUsageRepository(t *testing.T) {
	database := openTestDatabase(t)
	repository := gatewaysqlite.NewUsageRepository(database, 10)
	ctx := context.Background()
	event := domain.UsageEvent{
		RequestID: "req_1", CreatedAt: time.Now(), ModelAlias: "deepseek-v4-pro",
		Protocol: domain.ProtocolOpenAIResponses, UpstreamModel: "upstream", Status: domain.UsageSuccess,
		HTTPStatus: 200, Usage: domain.TokenUsage{
			InputTokens: 3, OutputTokens: 2, TotalTokens: 5,
			ProviderMetadata: map[string]any{"status": "completed", "api_key": "secret", "body": "private"},
		},
	}
	if err := repository.Insert(ctx, event); err != nil {
		t.Fatalf("Insert() error=%v", err)
	}
	duplicate := event
	duplicate.Status = domain.UsageError
	duplicate.ErrorCode = "should_not_replace"
	if err := repository.Insert(ctx, duplicate); err != nil {
		t.Fatalf("duplicate Insert() error=%v", err)
	}
	items, err := repository.List(ctx, "deepseek-v4-pro", 100)
	if err != nil || len(items) != 1 {
		t.Fatalf("List() items=%#v err=%v", items, err)
	}
	got := items[0]
	if got.Status != domain.UsageSuccess || got.ErrorCode != "" || got.Usage.TotalTokens != 5 {
		t.Fatalf("stored usage=%#v", got)
	}
	if got.ProviderMetadata["status"] != "completed" || got.ProviderMetadata["api_key"] != nil || got.ProviderMetadata["body"] != nil {
		t.Fatalf("metadata whitelist failed: %#v", got.ProviderMetadata)
	}
	if all, err := repository.List(ctx, "", 0); err != nil || len(all) != 1 {
		t.Fatalf("bounded List() all=%#v err=%v", all, err)
	}
}

func openTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := gatewaysqlite.Open(context.Background(), filepath.Join(t.TempDir(), "gateway.db"), 5*time.Second)
	if err != nil {
		t.Fatalf("Open() error=%v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}
