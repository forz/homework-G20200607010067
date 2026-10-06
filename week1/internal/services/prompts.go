package services

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/schema"
	"github.com/homework-G20200607010067/week1/internal/core"
	"github.com/homework-G20200607010067/week1/internal/domain"
)

var promptVariable = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)[^{}]*\}`)

// PromptStore is the storage boundary needed by PromptService.
type PromptStore interface {
	Create(context.Context, domain.PromptCreate) (*domain.PromptVersion, error)
	Get(context.Context, string, *int) (*domain.PromptVersion, error)
	List(context.Context, string, int) ([]domain.PromptVersion, error)
}

// RenderedPrompt is a rendered version plus its immutable identity.
type RenderedPrompt struct {
	ID      string      `json:"id"`
	Version int         `json:"version"`
	Role    domain.Role `json:"role"`
	Content string      `json:"content"`
}

// PromptService validates, stores, selects, and renders prompt versions.
type PromptService struct {
	store            PromptStore
	maxTemplateBytes int
	maxRenderedBytes int
}

// NewPromptService creates a strict Eino FString prompt service.
func NewPromptService(store PromptStore, maxTemplateBytes, maxRenderedBytes int) *PromptService {
	return &PromptService{store: store, maxTemplateBytes: maxTemplateBytes, maxRenderedBytes: maxRenderedBytes}
}

func (s *PromptService) Create(ctx context.Context, input domain.PromptCreate) (*domain.PromptVersion, error) {
	if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.Name) == "" || input.Content == "" {
		return nil, core.NewError(http.StatusBadRequest, core.ErrorTypeInvalidRequest, "invalid_prompt", "prompt id, name and content are required")
	}
	if input.Role == "" {
		input.Role = domain.RoleSystem
	}
	if input.Role != domain.RoleSystem && input.Role != domain.RoleDeveloper {
		return nil, core.NewError(http.StatusBadRequest, core.ErrorTypeInvalidRequest, "invalid_prompt_role", "prompt role must be system or developer")
	}
	if len(input.Content) > s.maxTemplateBytes {
		return nil, core.NewError(http.StatusBadRequest, core.ErrorTypeInvalidRequest, "prompt_too_large", "prompt template is too large")
	}
	return s.store.Create(ctx, input)
}

func (s *PromptService) Get(ctx context.Context, id string, version *int) (*domain.PromptVersion, error) {
	result, err := s.store.Get(ctx, id, version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.NewError(http.StatusNotFound, core.ErrorTypeInvalidRequest, "prompt_not_found", "prompt version was not found")
	}
	return result, err
}

func (s *PromptService) List(ctx context.Context, id string, limit int) ([]domain.PromptVersion, error) {
	return s.store.List(ctx, id, limit)
}

// Render preflights all placeholders, then delegates FString substitution to Eino.
func (s *PromptService) Render(ctx context.Context, id string, version *int, variables map[string]any) (*RenderedPrompt, error) {
	stored, err := s.Get(ctx, id, version)
	if err != nil {
		return nil, err
	}
	missing := make([]string, 0)
	seen := make(map[string]struct{})
	for _, match := range promptVariable.FindAllStringSubmatch(stored.Content, -1) {
		name := match[1]
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		if _, ok := variables[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, core.NewError(
			http.StatusUnprocessableEntity, core.ErrorTypeInvalidRequest, "missing_prompt_variables",
			"one or more required prompt variables are missing",
		).WithParam("variables")
	}
	template := prompt.FromMessages(schema.FString, &schema.Message{Role: schema.RoleType(stored.Role), Content: stored.Content})
	messages, err := template.Format(ctx, variables)
	if err != nil || len(messages) != 1 {
		return nil, core.WrapError(
			http.StatusUnprocessableEntity, core.ErrorTypeInvalidRequest, "prompt_render_failed", "prompt could not be rendered", err,
		)
	}
	if len(messages[0].Content) > s.maxRenderedBytes {
		return nil, core.NewError(http.StatusUnprocessableEntity, core.ErrorTypeInvalidRequest, "rendered_prompt_too_large", "rendered prompt is too large")
	}
	return &RenderedPrompt{ID: stored.ID, Version: stored.Version, Role: stored.Role, Content: messages[0].Content}, nil
}
