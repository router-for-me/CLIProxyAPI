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
	OfficialProvider           string            `json:"official_provider,omitempty"`
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
	// Pricing is the model's current pricing row (when known), attached
	// by the management ListModelsCatalog handler so the dashboard can
	// render an inline "$I / $O" summary per row without a per-row GET
	// /pricing call. Nil/zero when no pricing is set.
	Pricing *Pricing `json:"pricing,omitempty"`
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
		if err := s.upsertBatch(ctx, models[i:end], true); err != nil {
			return err
		}
	}
	return nil
}

const upsertBatchSize = 50

// upsertBatch writes a batch of models via INSERT ... ON CONFLICT. The
// protectUserDefined flag governs conflict behavior:
//   - true  (sync paths: /v1/models mirror, registry hooks): the UPDATE is
//     skipped when the existing row is user-defined, so operator edits made
//     via the dashboard are not clobbered by periodic auto-syncs.
//   - false (dashboard edit path): the row is always replaced so the
//     operator's explicit edit takes effect even on a user-defined row.
func (s *ModelsStore) upsertBatch(ctx context.Context, models []StoredModel, protectUserDefined bool) error {
	var b strings.Builder
	b.WriteString(`
		INSERT INTO `)
	b.WriteString(s.modelsTable)
	b.WriteString(` (
			id, provider, official_provider, object, created, owned_by, type, display_name,
			name, version, description, input_token_limit, output_token_limit,
			supported_generation_methods, context_length, max_completion_tokens,
			supported_parameters, input_modalities, output_modalities,
			supports_web_search, thinking_config, override_header, user_defined, updated_at
		) VALUES `)
	args := make([]any, 0, len(models)*24)
	const placeholders = 24
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
			m.ID, m.Provider, m.OfficialProvider, defaultIfEmpty(m.Object, "model"), m.Created,
			m.OwnedBy, m.Type, m.DisplayName, m.Name, m.Version, m.Description,
			m.InputTokenLimit, m.OutputTokenLimit, genMethods, m.ContextLength,
			m.MaxCompletionTokens, params, inputMod, outputMod,
			m.SupportsWebSearch, thinking, override, m.UserDefined, time.Now().UTC(),
		)
	}
	b.WriteString(`
		ON CONFLICT (id, provider) DO UPDATE SET
			official_provider = COALESCE(NULLIF(EXCLUDED.official_provider, ''), `)
	b.WriteString(s.modelsTable)
	b.WriteString(`.official_provider),
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
	if protectUserDefined {
		b.WriteString(`WHERE `)
		b.WriteString(s.modelsTable)
		b.WriteString(`.user_defined = FALSE
`)
	}
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
		SELECT id, provider, official_provider, object, created, owned_by, type, display_name,
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
		if err = rows.Scan(&m.ID, &m.Provider, &m.OfficialProvider, &m.Object, &m.Created, &m.OwnedBy,
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
// page and the count are scoped to that provider (the upstream/proxy
// provider). When officialProvider is non-empty, both are scoped to that
// official provider. When idFilter is non-empty, results are restricted to
// those IDs (typically populated from an availability registry, so the
// catalog page only shows live models).
func (s *ModelsStore) SelectAllPaged(ctx context.Context, page, pageSize int, provider, officialProvider string, idFilter []string) ([]StoredModel, int64, error) {
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
	if officialProvider != "" {
		args = append(args, officialProvider)
		whereParts = append(whereParts, fmt.Sprintf("official_provider = $%d", len(args)))
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
		SELECT id, provider, official_provider, object, created, owned_by, type, display_name,
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
		if err = rows.Scan(&m.ID, &m.Provider, &m.OfficialProvider, &m.Object, &m.Created, &m.OwnedBy,
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

// ModelsListFilter captures the optional filters + sort applied to a paged
// catalog list. Zero-value fields are no-ops. When Query is non-empty it is
// matched case-insensitively against id, name, display_name, and provider.
type ModelsListFilter struct {
	Provider         string   // = provider (upstream/proxy provider)
	OfficialProvider string   // = official_provider
	IDFilter         []string // restrict to these model IDs
	Query            string   // free-text ILIKE %q% against id/name/display_name/provider
}

// ModelsListSort identifies the ORDER BY column + direction. Column must be
// one of the allowed enum-style values; unknown values default to id asc.
type ModelsListSort struct {
	Column    string // "id" | "provider" | "official_provider" | "context_length" | "max_completion_tokens"
	Ascending bool
}

// SelectAllPagedFilter is the variant of SelectAllPaged that also supports
// the ModelsListFilter (free-text Query) and a ModelsListSort. Used by the
// dashboard's enhanced catalog page; the original SelectAllPaged stays
// available for callers (selectAllStreamed) that need the simpler shape.
func (s *ModelsStore) SelectAllPagedFilter(ctx context.Context, page, pageSize int, f ModelsListFilter, sort ModelsListSort) ([]StoredModel, int64, error) {
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
	if f.Provider != "" {
		args = append(args, f.Provider)
		whereParts = append(whereParts, fmt.Sprintf("provider = $%d", len(args)))
	}
	if f.OfficialProvider != "" {
		args = append(args, f.OfficialProvider)
		whereParts = append(whereParts, fmt.Sprintf("official_provider = $%d", len(args)))
	}
	if len(f.IDFilter) > 0 {
		args = append(args, pqStringArray(f.IDFilter))
		whereParts = append(whereParts, fmt.Sprintf("id = ANY($%d::text[])", len(args)))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+strings.ToLower(q)+"%")
		whereParts = append(whereParts, fmt.Sprintf(
			"(LOWER(id) LIKE $%d OR LOWER(COALESCE(name,'')) LIKE $%d OR LOWER(COALESCE(display_name,'')) LIKE $%d OR LOWER(provider) LIKE $%d)",
			len(args), len(args), len(args), len(args),
		))
	}
	whereClause := ""
	if len(whereParts) > 0 {
		whereClause = " WHERE " + strings.Join(whereParts, " AND ")
	}

	var total int64
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s%s`, s.modelsTable, whereClause)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres store: count models (paged-filter): %w", err)
	}

	orderClause := sortClause(sort)
	listArgs := append([]any{}, args...)
	listArgs = append(listArgs, pageSize, (page-1)*pageSize)
	listQuery := fmt.Sprintf(`
		SELECT id, provider, official_provider, object, created, owned_by, type, display_name,
		       name, version, description, input_token_limit, output_token_limit,
		       supported_generation_methods, context_length, max_completion_tokens,
		       supported_parameters, input_modalities, output_modalities,
		       supports_web_search, thinking_config, override_header, user_defined,
		       updated_at
		FROM %s%s
		ORDER BY %s LIMIT $%d OFFSET $%d
	`, s.modelsTable, whereClause, orderClause, len(listArgs)-1, len(listArgs))

	rows, err := s.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres store: select models (paged-filter): %w", err)
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
		if err = rows.Scan(&m.ID, &m.Provider, &m.OfficialProvider, &m.Object, &m.Created, &m.OwnedBy,
			&m.Type, &m.DisplayName, &m.Name, &m.Version, &m.Description,
			&m.InputTokenLimit, &m.OutputTokenLimit, &genMethods, &m.ContextLength,
			&m.MaxCompletionTokens, &params, &inputMod, &outputMod,
			&m.SupportsWebSearch, &thinking, &override, &m.UserDefined, &m.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("postgres store: scan model row (paged-filter): %w", err)
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

// SelectAllDistinctPagedFilter is the deduplicated variant of
// SelectAllPagedFilter: it returns at most one row per model id (the first
// matching row per id, ordered by id then provider). Used by the dashboard's
// allowed-models dropdown so each model id appears once regardless of how many
// upstream providers serve it. The same ModelsListFilter/ModelsListSort apply;
// the COUNT is COUNT(DISTINCT LOWER(id)).
func (s *ModelsStore) SelectAllDistinctPagedFilter(ctx context.Context, page, pageSize int, f ModelsListFilter, sort ModelsListSort) ([]StoredModel, int64, error) {
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
	if f.Provider != "" {
		args = append(args, f.Provider)
		whereParts = append(whereParts, fmt.Sprintf("provider = $%d", len(args)))
	}
	if f.OfficialProvider != "" {
		args = append(args, f.OfficialProvider)
		whereParts = append(whereParts, fmt.Sprintf("official_provider = $%d", len(args)))
	}
	if len(f.IDFilter) > 0 {
		args = append(args, pqStringArray(f.IDFilter))
		whereParts = append(whereParts, fmt.Sprintf("id = ANY($%d::text[])", len(args)))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+strings.ToLower(q)+"%")
		whereParts = append(whereParts, fmt.Sprintf(
			"(LOWER(id) LIKE $%d OR LOWER(COALESCE(name,'')) LIKE $%d OR LOWER(COALESCE(display_name,'')) LIKE $%d OR LOWER(provider) LIKE $%d)",
			len(args), len(args), len(args), len(args),
		))
	}
	whereClause := ""
	if len(whereParts) > 0 {
		whereClause = " WHERE " + strings.Join(whereParts, " AND ")
	}

	// DISTINCT ON requires its ORDER BY to start with the DISTINCT column. We
	// pick the first row per id ordered by id, provider so the result is
	// deterministic; a secondary sort by the user-requested column is applied
	// via an outer query wrapper so pagination math stays on distinct ids.
	var total int64
	countQuery := fmt.Sprintf(`SELECT COUNT(DISTINCT LOWER(id)) FROM %s%s`, s.modelsTable, whereClause)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres store: count models (distinct): %w", err)
	}

	orderClause := sortClause(sort)
	listArgs := append([]any{}, args...)
	listArgs = append(listArgs, pageSize, (page-1)*pageSize)
	// The inner SELECT picks one representative row per id; the outer SELECT
	// re-applies the requested sort column for display ordering and paginates.
	listQuery := fmt.Sprintf(`
		SELECT id, provider, official_provider, object, created, owned_by, type, display_name,
		       name, version, description, input_token_limit, output_token_limit,
		       supported_generation_methods, context_length, max_completion_tokens,
		       supported_parameters, input_modalities, output_modalities,
		       supports_web_search, thinking_config, override_header, user_defined,
		       updated_at
		FROM (
			SELECT DISTINCT ON (LOWER(id))
			       id, provider, official_provider, object, created, owned_by, type, display_name,
			       name, version, description, input_token_limit, output_token_limit,
			       supported_generation_methods, context_length, max_completion_tokens,
			       supported_parameters, input_modalities, output_modalities,
			       supports_web_search, thinking_config, override_header, user_defined,
			       updated_at
			FROM %s%s
			ORDER BY LOWER(id), provider
		) distinct_models
		ORDER BY %s LIMIT $%d OFFSET $%d
	`, s.modelsTable, whereClause, orderClause, len(listArgs)-1, len(listArgs))

	rows, err := s.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres store: select models (distinct-paged): %w", err)
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
		if err = rows.Scan(&m.ID, &m.Provider, &m.OfficialProvider, &m.Object, &m.Created, &m.OwnedBy,
			&m.Type, &m.DisplayName, &m.Name, &m.Version, &m.Description,
			&m.InputTokenLimit, &m.OutputTokenLimit, &genMethods, &m.ContextLength,
			&m.MaxCompletionTokens, &params, &inputMod, &outputMod,
			&m.SupportsWebSearch, &thinking, &override, &m.UserDefined, &m.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("postgres store: scan model row (distinct-paged): %w", err)
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

// sortClause maps a ModelsListSort to a safe ORDER BY clause. Column names
// are whitelisted to avoid SQL injection from query-string params.
func sortClause(sort ModelsListSort) string {
	col := strings.ToLower(strings.TrimSpace(sort.Column))
	switch col {
	case "provider":
		col = "provider"
	case "official_provider":
		col = "official_provider"
	case "context_length":
		col = "context_length"
	case "max_completion_tokens":
		col = "max_completion_tokens"
	case "display_name":
		col = "display_name"
	default:
		col = "id"
	}
	dir := "DESC"
	if sort.Ascending {
		dir = "ASC"
	}
	// NULLS LAST so empty values sort after populated ones regardless of
	// direction — matches the dashboard's "missing = bottom" UX.
	return col + " " + dir + " NULLS LAST"
}

// SelectByProvider returns models owned by the given provider.
func (s *ModelsStore) SelectByProvider(ctx context.Context, provider string) ([]StoredModel, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: models store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, provider, official_provider, object, created, owned_by, type, display_name,
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
		if err = rows.Scan(&m.ID, &m.Provider, &m.OfficialProvider, &m.Object, &m.Created, &m.OwnedBy,
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

// ModelsSummary summarizes the catalog for the dashboard header stat cards.
// Live is the count of catalog rows whose id appears in the in-memory
// registry; Priced is the count of rows with a non-zero model_pricing row;
// Unpriced = Total - Priced; Stale = Total - Live.
type ModelsSummary struct {
	Total    int64 `json:"total"`
	Live     int64 `json:"live"`
	Stale    int64 `json:"stale"`
	Priced   int64 `json:"priced"`
	Unpriced int64 `json:"unpriced"`
}

// Summary returns the catalog summary. The liveCount argument is supplied
// by the caller (from the in-memory registry) so the store stays decoupled
// from the registry package. pricedCount is computed via a LEFT JOIN against
// the model_pricing table.
func (s *ModelsStore) Summary(ctx context.Context, pricingTable string, liveIDs []string) (ModelsSummary, error) {
	if s == nil || s.db == nil {
		return ModelsSummary{}, fmt.Errorf("postgres store: models store not initialized")
	}
	summ := ModelsSummary{}
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, s.modelsTable)).Scan(&summ.Total); err != nil {
		return ModelsSummary{}, fmt.Errorf("postgres store: summary total: %w", err)
	}
	// Live count: catalog rows whose id is in the live set. Done in Go (not
	// SQL) because liveIDs is in-memory; copying them into a temp table just
	// for a count would be heavier than necessary for typical catalog sizes.
	liveSet := make(map[string]struct{}, len(liveIDs))
	for _, id := range liveIDs {
		liveSet[strings.ToLower(strings.TrimSpace(id))] = struct{}{}
	}
	var live int64
	liveRows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT LOWER(id) FROM %s`, s.modelsTable))
	if err != nil {
		return ModelsSummary{}, fmt.Errorf("postgres store: summary live scan: %w", err)
	}
	for liveRows.Next() {
		var id string
		if err := liveRows.Scan(&id); err != nil {
			liveRows.Close()
			return ModelsSummary{}, fmt.Errorf("postgres store: summary live scan row: %w", err)
		}
		if _, ok := liveSet[id]; ok {
			live++
		}
	}
	liveRows.Close()
	summ.Live = live
	summ.Stale = summ.Total - live
	// Priced: rows joined to model_pricing with at least one non-zero
	// component. Matches the loadCurrentPricing convention in the management
	// handler — an all-zero pricing row counts as unpriced.
	if pricingTable != "" {
		priceQuery := fmt.Sprintf(`
			SELECT COUNT(*)
			FROM %s m
			INNER JOIN %s p ON p.id = m.id
			WHERE COALESCE(p.input_per_1m_usd, 0) > 0
			   OR COALESCE(p.output_per_1m_usd, 0) > 0
			   OR COALESCE(p.cached_input_per_1m_usd, 0) > 0
			   OR COALESCE(p.cached_read_per_1m_usd, 0) > 0
			   OR COALESCE(p.reasoning_per_1m_usd, 0) > 0
		`, s.modelsTable, pricingTable)
		if err := s.db.QueryRowContext(ctx, priceQuery).Scan(&summ.Priced); err != nil {
			return ModelsSummary{}, fmt.Errorf("postgres store: summary priced: %w", err)
		}
	}
	summ.Unpriced = summ.Total - summ.Priced
	return summ, nil
}

// Distinct returns the unique non-empty values for the requested column.
// column is whitelisted: only "provider" and "official_provider" are
// accepted; any other value returns an error to avoid SQL injection.
func (s *ModelsStore) Distinct(ctx context.Context, column string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: models store not initialized")
	}
	col := strings.ToLower(strings.TrimSpace(column))
	switch col {
	case "provider", "official_provider":
	default:
		return nil, fmt.Errorf("postgres store: distinct column %q not allowed", column)
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT DISTINCT %s FROM %s WHERE %s <> '' ORDER BY %s`, col, s.modelsTable, col, col,
	))
	if err != nil {
		return nil, fmt.Errorf("postgres store: distinct %s: %w", col, err)
	}
	defer rows.Close()
	out := make([]string, 0, 16)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("postgres store: distinct %s scan: %w", col, err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
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
// Unlike the sync path, this always replaces the existing row so the
// operator's explicit edit wins even on a previously user-defined row.
func (s *ModelsStore) UpsertOne(ctx context.Context, m StoredModel) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: models store not initialized")
	}
	if strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.Provider) == "" {
		return fmt.Errorf("postgres store: upsert model requires non-empty id and provider")
	}
	return s.upsertBatch(ctx, []StoredModel{m}, false)
}

// SelectOne returns a single model row by (id, provider) primary key. Returns
// ErrModelNotFound when no row matches.
func (s *ModelsStore) SelectOne(ctx context.Context, id, provider string) (*StoredModel, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: models store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT id, provider, official_provider, object, created, owned_by, type, display_name,
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
	err := row.Scan(&m.ID, &m.Provider, &m.OfficialProvider, &m.Object, &m.Created, &m.OwnedBy,
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

// OfficialProviderByModelAndProvider returns the official_provider column for
// the (id, provider) row in the models catalog. The provider argument is the
// catalog's provider column value (the model owner, e.g. "anthropic" or
// "opencode"), not the internal provider key. Returns ("", nil) when no row
// matches so callers can fall back to other sources.
func (s *ModelsStore) OfficialProviderByModelAndProvider(ctx context.Context, id, provider string) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("postgres store: models store not initialized")
	}
	var official string
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT official_provider FROM %s WHERE id = $1 AND provider = $2`,
		s.modelsTable,
	), id, provider).Scan(&official)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("postgres store: select official_provider: %w", err)
	}
	return official, nil
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
