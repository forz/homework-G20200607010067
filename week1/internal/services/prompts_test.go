package services

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

func TestPromptService(t *testing.T) {
	store := &promptStoreFake{stored: &domain.PromptVersion{
		ID: "welcome", Version: 2, Name: "Welcome", Role: domain.RoleSystem,
		Content: "Hello {name}", IsActive: true, CreatedAt: time.Now(),
	}}
	service := NewPromptService(store, 64, 64)

	t.Run("create defaults the role to system", func(t *testing.T) {
		created, err := service.Create(context.Background(), domain.PromptCreate{ID: "welcome", Name: "Welcome", Content: "Hello {name}"})
		if err != nil || created.Role != domain.RoleSystem || store.created.Role != domain.RoleSystem {
			t.Fatalf("Create() created=%#v err=%v stored=%#v", created, err, store.created)
		}
	})

	t.Run("render preflights variables and preserves version", func(t *testing.T) {
		rendered, err := service.Render(context.Background(), "welcome", nil, map[string]any{"name": "Alice"})
		if err != nil || rendered.Content != "Hello Alice" || rendered.Version != 2 {
			t.Fatalf("Render() result=%#v err=%v", rendered, err)
		}
		if _, err := service.Render(context.Background(), "welcome", nil, nil); err == nil || core.NormalizeError(err).Code != "missing_prompt_variables" {
			t.Fatalf("missing variable error=%v", err)
		}
	})

	t.Run("maps missing versions and enforces bounds", func(t *testing.T) {
		store.getErr = sql.ErrNoRows
		if _, err := service.Get(context.Background(), "missing", nil); err == nil || core.NormalizeError(err).Code != "prompt_not_found" {
			t.Fatalf("missing prompt error=%v", err)
		}
		store.getErr = nil
		if _, err := service.Create(context.Background(), domain.PromptCreate{ID: "x", Name: "x", Role: domain.RoleUser, Content: "x"}); err == nil {
			t.Fatal("unsupported prompt role was accepted")
		}
		if _, err := NewPromptService(store, 2, 64).Create(context.Background(), domain.PromptCreate{ID: "x", Name: "x", Content: "long"}); err == nil {
			t.Fatal("oversized prompt was accepted")
		}
	})
}

type promptStoreFake struct {
	stored  *domain.PromptVersion
	created domain.PromptCreate
	getErr  error
}

func (s *promptStoreFake) Create(_ context.Context, input domain.PromptCreate) (*domain.PromptVersion, error) {
	s.created = input
	return &domain.PromptVersion{ID: input.ID, Version: 1, Name: input.Name, Role: input.Role, Content: input.Content, IsActive: true}, nil
}

func (s *promptStoreFake) Get(context.Context, string, *int) (*domain.PromptVersion, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	copyOf := *s.stored
	return &copyOf, nil
}

func (s *promptStoreFake) List(context.Context, string, int) ([]domain.PromptVersion, error) {
	return []domain.PromptVersion{*s.stored}, nil
}
