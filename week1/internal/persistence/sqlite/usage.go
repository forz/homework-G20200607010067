package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/homework-G20200607010067/week1/internal/domain"
)

const maxProviderMetadataBytes = 2048

var allowedProviderMetadata = map[string]struct{}{
	"status": {}, "service_tier": {}, "finish_reason": {},
}

// UsageRepository persists privacy-safe terminal usage records.
type UsageRepository struct {
	db       *sql.DB
	maxLimit int
}

func NewUsageRepository(db *sql.DB, maxLimit int) *UsageRepository {
	return &UsageRepository{db: db, maxLimit: maxLimit}
}

// Insert writes at most one event per gateway request ID.
func (r *UsageRepository) Insert(ctx context.Context, event domain.UsageEvent) error {
	metadata := sanitizeMetadata(event.ProviderMetadata)
	if len(metadata) == 0 {
		metadata = sanitizeMetadata(event.Usage.ProviderMetadata)
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode usage metadata: %w", err)
	}
	if len(encoded) > maxProviderMetadataBytes {
		encoded = []byte("{}")
	}
	_, err = r.db.ExecContext(ctx, `INSERT OR IGNORE INTO usage_events
        (request_id, created_at, model_alias, protocol, upstream_model, stream, status, http_status,
         input_tokens, output_tokens, total_tokens, cached_tokens, cache_write_tokens, reasoning_tokens,
         usage_incomplete, latency_ms, first_token_ms, transport_retries, structured_corrections,
         prompt_id, prompt_version, error_type, error_code, provider_metadata_json)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.RequestID, event.CreatedAt.UTC().Format(time.RFC3339Nano), event.ModelAlias, event.Protocol,
		event.UpstreamModel, event.Stream, event.Status, event.HTTPStatus,
		event.Usage.InputTokens, event.Usage.OutputTokens, event.Usage.TotalTokens,
		event.Usage.CachedTokens, event.Usage.CacheWriteTokens, event.Usage.ReasoningTokens,
		event.Usage.Incomplete, event.LatencyMS, event.FirstTokenMS, event.TransportRetries,
		event.StructuredCorrections, event.PromptID, event.PromptVersion, event.ErrorType,
		event.ErrorCode, string(encoded),
	)
	if err != nil {
		return fmt.Errorf("insert usage event: %w", err)
	}
	return nil
}

// List returns recent usage records, optionally filtered by public model alias.
func (r *UsageRepository) List(ctx context.Context, model string, limit int) ([]domain.UsageEvent, error) {
	limit = min(max(limit, 1), r.maxLimit)
	query := usageSelect + " ORDER BY created_at DESC LIMIT ?"
	arguments := []any{limit}
	if model != "" {
		query = usageSelect + " WHERE model_alias = ? ORDER BY created_at DESC LIMIT ?"
		arguments = []any{model, limit}
	}
	rows, err := r.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("query usage events: %w", err)
	}
	defer rows.Close()
	items := make([]domain.UsageEvent, 0)
	for rows.Next() {
		item, scanErr := scanUsage(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage events: %w", err)
	}
	return items, nil
}

const usageSelect = `SELECT request_id, created_at, model_alias, protocol, upstream_model, stream,
    status, http_status, input_tokens, output_tokens, total_tokens, cached_tokens,
    cache_write_tokens, reasoning_tokens, usage_incomplete, latency_ms, first_token_ms,
    transport_retries, structured_corrections, prompt_id, prompt_version, error_type,
    error_code, provider_metadata_json FROM usage_events`

func scanUsage(scanner promptScanner) (*domain.UsageEvent, error) {
	var event domain.UsageEvent
	var createdAt, protocol, status, metadataJSON string
	var firstToken sql.NullFloat64
	var promptVersion sql.NullInt64
	if err := scanner.Scan(
		&event.RequestID, &createdAt, &event.ModelAlias, &protocol, &event.UpstreamModel,
		&event.Stream, &status, &event.HTTPStatus, &event.Usage.InputTokens,
		&event.Usage.OutputTokens, &event.Usage.TotalTokens, &event.Usage.CachedTokens,
		&event.Usage.CacheWriteTokens, &event.Usage.ReasoningTokens, &event.Usage.Incomplete,
		&event.LatencyMS, &firstToken, &event.TransportRetries, &event.StructuredCorrections,
		&event.PromptID, &promptVersion, &event.ErrorType, &event.ErrorCode, &metadataJSON,
	); err != nil {
		return nil, fmt.Errorf("scan usage event: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse usage created time: %w", err)
	}
	event.CreatedAt = parsed
	event.Protocol = domain.Protocol(protocol)
	event.Status = domain.UsageStatus(status)
	if firstToken.Valid {
		event.FirstTokenMS = &firstToken.Float64
	}
	if promptVersion.Valid {
		value := int(promptVersion.Int64)
		event.PromptVersion = &value
	}
	if err := json.Unmarshal([]byte(metadataJSON), &event.ProviderMetadata); err != nil {
		return nil, fmt.Errorf("decode usage metadata: %w", err)
	}
	return &event, nil
}

func sanitizeMetadata(input map[string]any) map[string]any {
	result := make(map[string]any)
	for key, value := range input {
		if _, allowed := allowedProviderMetadata[key]; !allowed {
			continue
		}
		switch typed := value.(type) {
		case string:
			result[key] = truncateMetadata(typed)
		case bool, float64, float32, int, int32, int64, uint, uint32, uint64:
			result[key] = typed
		}
	}
	return result
}

func truncateMetadata(value string) string {
	value = strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, value)
	if len(value) > 256 {
		return value[:256]
	}
	return value
}
