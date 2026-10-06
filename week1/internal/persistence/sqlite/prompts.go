package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/homework-G20200607010067/week1/internal/domain"
)

// PromptRepository persists immutable prompt versions in SQLite.
type PromptRepository struct {
	db       *sql.DB
	maxLimit int
}

// NewPromptRepository creates a bounded prompt repository.
func NewPromptRepository(db *sql.DB, maxLimit int) *PromptRepository {
	return &PromptRepository{db: db, maxLimit: maxLimit}
}

// Create serializes version allocation with BEGIN IMMEDIATE and optionally
// moves the unique active marker to the new immutable version.
func (r *PromptRepository) Create(ctx context.Context, input domain.PromptCreate) (*domain.PromptVersion, error) {
	connection, err := r.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire prompt database connection: %w", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("begin prompt version transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()
	var version int
	if err := connection.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) + 1 FROM prompt_versions WHERE prompt_id = ?", input.ID,
	).Scan(&version); err != nil {
		return nil, fmt.Errorf("allocate prompt version: %w", err)
	}
	activate := input.Activate || version == 1
	if activate {
		if _, err := connection.ExecContext(ctx, "UPDATE prompt_versions SET is_active = 0 WHERE prompt_id = ?", input.ID); err != nil {
			return nil, fmt.Errorf("deactivate previous prompt version: %w", err)
		}
	}
	createdAt := time.Now().UTC()
	if _, err := connection.ExecContext(ctx, `INSERT INTO prompt_versions
        (prompt_id, version, name, description, role, content, is_active, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		input.ID, version, input.Name, input.Description, input.Role, input.Content, activate, createdAt.Format(time.RFC3339Nano),
	); err != nil {
		return nil, fmt.Errorf("insert prompt version: %w", err)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("commit prompt version: %w", err)
	}
	committed = true
	return &domain.PromptVersion{
		ID: input.ID, Version: version, Name: input.Name, Description: input.Description,
		Role: input.Role, Content: input.Content, IsActive: activate, CreatedAt: createdAt,
	}, nil
}

// Get returns an explicit version, or the active version when version is nil.
func (r *PromptRepository) Get(ctx context.Context, id string, version *int) (*domain.PromptVersion, error) {
	query := `SELECT prompt_id, version, name, description, role, content, is_active, created_at
        FROM prompt_versions WHERE prompt_id = ? AND is_active = 1`
	arguments := []any{id}
	if version != nil {
		query = `SELECT prompt_id, version, name, description, role, content, is_active, created_at
            FROM prompt_versions WHERE prompt_id = ? AND version = ?`
		arguments = append(arguments, *version)
	}
	return scanPrompt(r.db.QueryRowContext(ctx, query, arguments...))
}

// List returns newest versions first with a hard upper bound.
func (r *PromptRepository) List(ctx context.Context, id string, limit int) ([]domain.PromptVersion, error) {
	limit = min(max(limit, 1), r.maxLimit)
	query := `SELECT prompt_id, version, name, description, role, content, is_active, created_at
        FROM prompt_versions ORDER BY created_at DESC LIMIT ?`
	arguments := []any{limit}
	if id != "" {
		query = `SELECT prompt_id, version, name, description, role, content, is_active, created_at
            FROM prompt_versions WHERE prompt_id = ? ORDER BY version DESC LIMIT ?`
		arguments = []any{id, limit}
	}
	rows, err := r.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("query prompt versions: %w", err)
	}
	defer rows.Close()
	result := make([]domain.PromptVersion, 0)
	for rows.Next() {
		promptVersion, scanErr := scanPrompt(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, *promptVersion)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate prompt versions: %w", err)
	}
	return result, nil
}

type promptScanner interface{ Scan(...any) error }

func scanPrompt(scanner promptScanner) (*domain.PromptVersion, error) {
	var result domain.PromptVersion
	var role string
	var active bool
	var createdAt string
	if err := scanner.Scan(
		&result.ID, &result.Version, &result.Name, &result.Description,
		&role, &result.Content, &active, &createdAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("scan prompt version: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse prompt created time: %w", err)
	}
	result.Role = domain.Role(role)
	result.IsActive = active
	result.CreatedAt = parsed
	return &result, nil
}
