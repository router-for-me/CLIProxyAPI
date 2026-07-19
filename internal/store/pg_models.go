package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// StoredModel mirrors a row in models_catalog. It is a store-level type rather
// than an alias for registry.ModelInfo to avoid an import cycle (the registry
// package depends on many internal packages and the store layer must stay
// dependency-light). The registry/pg_sync.go adapter converts between the two
// representations.
type StoredModel struct {
	ID                         string            `json:"id"`
	Provider                   string            `json:"provider"`
	Object                     string            `json:"object"`
	Created                    int64             `json:"created"`
	OwnedBy                    string            `json:"owned_by"`
	Type                       string            `json:"type"`
	DisplayName                string            `json:"display_name,omitempty"`
	Name                       string            `json:"name,omitempty"`
	Version                    string            `json:"version,omitempty"`
	Description                string            `json:"description,omitempty"`
	InputTokenLimit            int               `json:"input_token_limit,omitempty"`
	OutputTokenLimit           int               `json:"output_token_limit,omitempty"`
	SupportedGenerationMethods []string          `json:"supported_generation_methods,omitempty"`
	ContextLength              int               `json:"context_length,omitempty"`
	MaxCompletionTokens        int               `json:"max_completion_tokens,omitempty"`
	SupportedParameters        []string          `json:"supported_parameters,omitempty"`
	InputModalities            []string          `json:"input_modalities,omitempty"`
	OutputModalities           []string          `json:"output_modalities,omitempty"`
	SupportsWebSearch          bool              `json:"supports_web_search,omitempty"`
	Thinking                   map[string]any    `json:"thinking,omitempty"`
	OverrideHeader             map[string]string `json:"override_header,omitempty"`
	UserDefined                bool              `json:"user_defined,omitempty"`
	UpdatedAt                  time.Time         `json:"updated_at"`
}

// ModelsStore wraps the database handle for model catalog persistence.
type ModelsStore struct {
	db          *sql.DB
	modelsTable string
}

// NewModelsStore builds a ModelsStore from a PostgresStore. Returns nil when the
// parent store is nil for feature-detection via nil check.
func NewModelsStore(parent *PostgresStore) *ModelsStore {
	if parent == nil {
		return nil
	}
	return &ModelsStore{
		db:          parent.DB(),
		modelsTable: parent.ModelsTable(),
	}
}

// UpsertModels writes the supplied models to models_catalog, updating existing
// rows on (id, provider) conflicts. The batch is split into chunks of
// upsertBatchSize to avoid parameter count limits.
func (s *ModelsStore) UpsertModels(ctx context.Context, models []StoredModel) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: models store not initialized")
	}
	if len(models) == 0 {
		return nil
	}
	for i := 0; i < len(models); i += upsertBatchSize {
		end := i + upsertBatchSize
		if end > len(models) {
			end = len(models)
		}
		if err := s.upsertBatch(ctx, models[i:end]); err != nil {
			return err
		}
	}
	return nil
}

const upsertBatchSize = 50

func (s *ModelsStore) upsertBatch(ctx context.Context, models []StoredModel) error {
	var b strings.Builder
	b.WriteString(`
		INSERT INTO `)
	b.WriteString(s.modelsTable)
	b.WriteString(` (
			id, provider, object, created, owned_by, type, display_name,
			name, version, description, input_token_limit, output_token_limit,
			supported_generation_methods, context_length, max_completion_tokens,
			supported_parameters, input_modalities, output_modalities,
			supports_web_search, thinking_config, override_header, user_defined, updated_at
		) VALUES `)
	args := make([]any, 0, len(models)*23)
	const placeholders = 23
	for i, m := range models {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('(')
		for j := 1; j <= placeholders; j++ {
			if j > 1 {
				b.WriteByte(',')
			}
			b.WriteByte('$')
			b.WriteString(itoa(i*placeholders + j))
		}
		b.WriteByte(')')

		thinking, err := marshalJSONField(m.Thinking)
		if err != nil {
			return fmt.Errorf("postgres store: marshal thinking config: %w", err)
		}
		override, err := marshalJSONField(m.OverrideHeader)
		if err != nil {
			return fmt.Errorf("postgres store: marshal override header: %w", err)
		}
		genMethods, err := marshalJSONField(m.SupportedGenerationMethods)
		if err != nil {
			return fmt.Errorf("postgres store: marshal generation methods: %w", err)
		}
		params, err := marshalJSONField(m.SupportedParameters)
		if err != nil {
			return fmt.Errorf("postgres store: marshal supported params: %w", err)
		}
		inputMod, err := marshalJSONField(m.InputModalities)
		if err != nil {
			return fmt.Errorf("postgres store: marshal input modalities: %w", err)
		}
		outputMod, err := marshalJSONField(m.OutputModalities)
		if err != nil {
			return fmt.Errorf("postgres store: marshal output modalities: %w", err)
		}

		args = append(args,
			m.ID, m.Provider, defaultIfEmpty(m.Object, "model"), m.Created,
			m.OwnedBy, m.Type, m.DisplayName, m.Name, m.Version, m.Description,
			m.InputTokenLimit, m.OutputTokenLimit, genMethods, m.ContextLength,
			m.MaxCompletionTokens, params, inputMod, outputMod,
			m.SupportsWebSearch, thinking, override, m.UserDefined, time.Now().UTC(),
		)
	}
	b.WriteString(`
		ON CONFLICT (id, provider) DO UPDATE SET
			object = EXCLUDED.object,
			created = EXCLUDED.created,
			owned_by = EXCLUDED.owned_by,
			type = EXCLUDED.type,
			display_name = EXCLUDED.display_name,
			name = EXCLUDED.name,
			version = EXCLUDED.version,
			description = EXCLUDED.description,
			input_token_limit = EXCLUDED.input_token_limit,
			output_token_limit = EXCLUDED.output_token_limit,
			supported_generation_methods = EXCLUDED.supported_generation_methods,
			context_length = EXCLUDED.context_length,
			max_completion_tokens = EXCLUDED.max_completion_tokens,
			supported_parameters = EXCLUDED.supported_parameters,
			input_modalities = EXCLUDED.input_modalities,
			output_modalities = EXCLUDED.output_modalities,
			supports_web_search = EXCLUDED.supports_web_search,
			thinking_config = EXCLUDED.thinking_config,
			override_header = EXCLUDED.override_header,
			user_defined = EXCLUDED.user_defined,
			updated_at = NOW()
	`)
	if _, err := s.db.ExecContext(ctx, b.String(), args...); err != nil {
		return fmt.Errorf("postgres store: upsert models batch: %w", err)
	}
	return nil
}

func marshalJSONField(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" {
		return nil, nil
	}
	return string(raw), nil
}

func defaultIfEmpty(value, def string) string {
	if value == "" {
		return def
	}
	return value
}

// SelectAll returns every model row in the catalog, ordered by provider then ID.
func (s *ModelsStore) SelectAll(ctx context.Context) ([]StoredModel, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: models store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, provider, object, created, owned_by, type, display_name,
		       name, version, description, input_token_limit, output_token_limit,
		       supported_generation_methods, context_length, max_completion_tokens,
		       supported_parameters, input_modalities, output_modalities,
		       supports_web_search, thinking_config, override_header, user_defined,
		       updated_at
		FROM %s
		ORDER BY provider, id
	`, s.modelsTable))
	if err != nil {
		return nil, fmt.Errorf("postgres store: select models: %w", err)
	}
	defer rows.Close()
	out := make([]StoredModel, 0, 64)
	for rows.Next() {
		var (
			m          StoredModel
			genMethods []byte
			params     []byte
			inputMod   []byte
			outputMod  []byte
			thinking   []byte
			override   []byte
		)
		if err = rows.Scan(&m.ID, &m.Provider, &m.Object, &m.Created, &m.OwnedBy,
			&m.Type, &m.DisplayName, &m.Name, &m.Version, &m.Description,
			&m.InputTokenLimit, &m.OutputTokenLimit, &genMethods, &m.ContextLength,
			&m.MaxCompletionTokens, &params, &inputMod, &outputMod,
			&m.SupportsWebSearch, &thinking, &override, &m.UserDefined, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("postgres store: scan model row: %w", err)
		}
		m.SupportedGenerationMethods = decodeStringArray(genMethods)
		m.SupportedParameters = decodeStringArray(params)
		m.InputModalities = decodeStringArray(inputMod)
		m.OutputModalities = decodeStringArray(outputMod)
		if len(thinking) > 0 && string(thinking) != "null" {
			_ = json.Unmarshal(thinking, &m.Thinking)
		}
		if len(override) > 0 && string(override) != "null" {
			_ = json.Unmarshal(override, &m.OverrideHeader)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SelectAllPaged returns one page of models plus the total count. Page is
// 1-indexed; pageSize must be > 0. When provider is non-empty, both the
// page and the count are scoped to that provider. When idFilter is non-empty,
// results are restricted to those IDs (typically populated from an
// availability registry, so the catalog page only shows live models).
func (s *ModelsStore) SelectAllPaged(ctx context.Context, page, pageSize int, provider string, idFilter []string) ([]StoredModel, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("postgres store: models store not initialized")
	}
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 25
	}

	var (
		whereParts []string
		args       []any
	)
	if provider != "" {
		args = append(args, provider)
		whereParts = append(whereParts, fmt.Sprintf("provider = $%d", len(args)))
	}
	if len(idFilter) > 0 {
		// Build an ANY(text[]) predicate; PostgreSQL handles it natively
		// without parameter-count exhaustion for large ID lists.
		args = append(args, pqStringArray(idFilter))
		whereParts = append(whereParts, fmt.Sprintf("id = ANY($%d::text[])", len(args)))
	}
	whereClause := ""
	if len(whereParts) > 0 {
		whereClause = " WHERE " + strings.Join(whereParts, " AND ")
	}

	var total int64
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s%s`, s.modelsTable, whereClause)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres store: count models (paged): %w", err)
	}

	listArgs := append([]any{}, args...)
	listArgs = append(listArgs, pageSize, (page-1)*pageSize)
	listQuery := fmt.Sprintf(`
		SELECT id, provider, object, created, owned_by, type, display_name,
		       name, version, description, input_token_limit, output_token_limit,
		       supported_generation_methods, context_length, max_completion_tokens,
		       supported_parameters, input_modalities, output_modalities,
		       supports_web_search, thinking_config, override_header, user_defined,
		       updated_at
		FROM %s%s
		ORDER BY provider, id LIMIT $%d OFFSET $%d
	`, s.modelsTable, whereClause, len(listArgs)-1, len(listArgs))

	rows, err := s.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres store: select models (paged): %w", err)
	}
	defer rows.Close()
	out := make([]StoredModel, 0, pageSize)
	for rows.Next() {
		var (
			m          StoredModel
			genMethods []byte
			params     []byte
			inputMod   []byte
			outputMod  []byte
			thinking   []byte
			override   []byte
		)
		if err = rows.Scan(&m.ID, &m.Provider, &m.Object, &m.Created, &m.OwnedBy,
			&m.Type, &m.DisplayName, &m.Name, &m.Version, &m.Description,
			&m.InputTokenLimit, &m.OutputTokenLimit, &genMethods, &m.ContextLength,
			&m.MaxCompletionTokens, &params, &inputMod, &outputMod,
			&m.SupportsWebSearch, &thinking, &override, &m.UserDefined, &m.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("postgres store: scan model row (paged): %w", err)
		}
		m.SupportedGenerationMethods = decodeStringArray(genMethods)
		m.SupportedParameters = decodeStringArray(params)
		m.InputModalities = decodeStringArray(inputMod)
		m.OutputModalities = decodeStringArray(outputMod)
		if len(thinking) > 0 && string(thinking) != "null" {
			_ = json.Unmarshal(thinking, &m.Thinking)
		}
		if len(override) > 0 && string(override) != "null" {
			_ = json.Unmarshal(override, &m.OverrideHeader)
		}
		out = append(out, m)
	}
	return out, total, rows.Err()
}

// pqStringArray formats a Go []string as a PostgreSQL text[] literal
// compatible with the = ANY($n::text[]) parameter binding. We use array
// literals instead of variadic placeholders so the parameter count stays
// bounded regardless of ID list size.
func pqStringArray(ids []string) any {
	// Quote each element defensively; this guards against IDs containing
	// commas or special characters. The driver receives a JSON-shape that
	// pgx maps to a text[] via the ::text[] cast.
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.ReplaceAll(id, `"`, `\"`)
		parts = append(parts, `"`+id+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// SelectByProvider returns models owned by the given provider.
func (s *ModelsStore) SelectByProvider(ctx context.Context, provider string) ([]StoredModel, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: models store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, provider, object, created, owned_by, type, display_name,
		       name, version, description, input_token_limit, output_token_limit,
		       supported_generation_methods, context_length, max_completion_tokens,
		       supported_parameters, input_modalities, output_modalities,
		       supports_web_search, thinking_config, override_header, user_defined,
		       updated_at
		FROM %s
		WHERE provider = $1
		ORDER BY id
	`, s.modelsTable), provider)
	if err != nil {
		return nil, fmt.Errorf("postgres store: select models by provider: %w", err)
	}
	defer rows.Close()
	out := make([]StoredModel, 0, 16)
	for rows.Next() {
		var (
			m          StoredModel
			genMethods []byte
			params     []byte
			inputMod   []byte
			outputMod  []byte
			thinking   []byte
			override   []byte
		)
		if err = rows.Scan(&m.ID, &m.Provider, &m.Object, &m.Created, &m.OwnedBy,
			&m.Type, &m.DisplayName, &m.Name, &m.Version, &m.Description,
			&m.InputTokenLimit, &m.OutputTokenLimit, &genMethods, &m.ContextLength,
			&m.MaxCompletionTokens, &params, &inputMod, &outputMod,
			&m.SupportsWebSearch, &thinking, &override, &m.UserDefined, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("postgres store: scan model row (provider): %w", err)
		}
		m.SupportedGenerationMethods = decodeStringArray(genMethods)
		m.SupportedParameters = decodeStringArray(params)
		m.InputModalities = decodeStringArray(inputMod)
		m.OutputModalities = decodeStringArray(outputMod)
		if len(thinking) > 0 && string(thinking) != "null" {
			_ = json.Unmarshal(thinking, &m.Thinking)
		}
		if len(override) > 0 && string(override) != "null" {
			_ = json.Unmarshal(override, &m.OverrideHeader)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Count returns the total number of model rows in the catalog. Used during
// bootstrap to decide whether to seed from the embedded JSON.
func (s *ModelsStore) Count(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: models store not initialized")
	}
	var count int64
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, s.modelsTable)).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("postgres store: count models: %w", err)
	}
	return count, nil
}

// DeleteByProvider removes all model rows for the supplied provider. Used when
// the registry updater detects that a provider's catalog changed wholesale.
func (s *ModelsStore) DeleteByProvider(ctx context.Context, provider string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: models store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE provider = $1`, s.modelsTable,
	), provider)
	if err != nil {
		return 0, fmt.Errorf("postgres store: delete models by provider: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("postgres store: delete models rows affected: %w", err)
	}
	return n, nil
}

// UpsertOne inserts or updates a single model row. Use this for ad-hoc edits
// from the dashboard (e.g. an operator adjusting display_name, context_length,
// or adding a user-defined model not present in the upstream registry).
func (s *ModelsStore) UpsertOne(ctx context.Context, m StoredModel) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: models store not initialized")
	}
	if strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.Provider) == "" {
		return fmt.Errorf("postgres store: upsert model requires non-empty id and provider")
	}
	return s.upsertBatch(ctx, []StoredModel{m})
}

// SelectOne returns a single model row by (id, provider) primary key. Returns
// ErrModelNotFound when no row matches.
func (s *ModelsStore) SelectOne(ctx context.Context, id, provider string) (*StoredModel, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: models store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT id, provider, object, created, owned_by, type, display_name,
		       name, version, description, input_token_limit, output_token_limit,
		       supported_generation_methods, context_length, max_completion_tokens,
		       supported_parameters, input_modalities, output_modalities,
		       supports_web_search, thinking_config, override_header, user_defined,
		       updated_at
		FROM %s WHERE id = $1 AND provider = $2
	`, s.modelsTable), id, provider)
	var (
		m          StoredModel
		genMethods []byte
		params     []byte
		inputMod   []byte
		outputMod  []byte
		thinking   []byte
		override   []byte
	)
	err := row.Scan(&m.ID, &m.Provider, &m.Object, &m.Created, &m.OwnedBy,
		&m.Type, &m.DisplayName, &m.Name, &m.Version, &m.Description,
		&m.InputTokenLimit, &m.OutputTokenLimit, &genMethods, &m.ContextLength,
		&m.MaxCompletionTokens, &params, &inputMod, &outputMod,
		&m.SupportsWebSearch, &thinking, &override, &m.UserDefined, &m.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrModelNotFound
		}
		return nil, fmt.Errorf("postgres store: select model: %w", err)
	}
	m.SupportedGenerationMethods = decodeStringArray(genMethods)
	m.SupportedParameters = decodeStringArray(params)
	m.InputModalities = decodeStringArray(inputMod)
	m.OutputModalities = decodeStringArray(outputMod)
	if len(thinking) > 0 && string(thinking) != "null" {
		_ = json.Unmarshal(thinking, &m.Thinking)
	}
	if len(override) > 0 && string(override) != "null" {
		_ = json.Unmarshal(override, &m.OverrideHeader)
	}
	return &m, nil
}

// DeleteOne removes a single model row by (id, provider) primary key.
// Returns ErrModelNotFound when no row matched.
func (s *ModelsStore) DeleteOne(ctx context.Context, id, provider string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: models store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE id = $1 AND provider = $2`, s.modelsTable,
	), id, provider)
	if err != nil {
		return fmt.Errorf("postgres store: delete model: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres store: delete model rows affected: %w", err)
	}
	if n == 0 {
		return ErrModelNotFound
	}
	return nil
}

// ErrModelNotFound is returned when a single model lookup finds no row.
var ErrModelNotFound = errors.New("postgres store: model not found")
