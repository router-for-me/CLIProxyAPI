package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
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
	db           *sql.DB
	modelsTable  string
	routingTable string

	// routeCache caches global per-model routing overrides (ModelRoute keyed by
	// lowercased model id) in memory so the request-time routing decision never
	// hits the DB after the first access. A nil value / missing key caches a
	// negative result ("no global route for this model"). Guarded by routeMu.
	routeMu    sync.RWMutex
	routeCache map[string]*ModelRoute
}

// NewModelsStore builds a ModelsStore from a PostgresStore. Returns nil when the
// parent store is nil for feature-detection via nil check.
func NewModelsStore(parent *PostgresStore) *ModelsStore {
	if parent == nil {
		return nil
	}
	return &ModelsStore{
		db:           parent.DB(),
		modelsTable:  parent.ModelsTable(),
		routingTable: parent.ModelRoutingTable(),
		routeCache:   make(map[string]*ModelRoute),
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
			official_provider = `)
	// official_provider is an operator-facing correction field (e.g. mapping
	// the internal "antigravity" key to "anthropic"). Auto-syncs derive it
	// verbatim from OwnedBy, so blindly writing EXCLUDED.official_provider
	// would clobber an operator edit every time the upstream re-registers.
	// Preserve the persisted value on the auto-sync path (protectUserDefined);
	// only the dashboard PUT path (protectUserDefined=false) overwrites it,
	// using COALESCE(NULLIF(...)) so an empty body value keeps the existing
	// value intact.
	if protectUserDefined {
		b.WriteString(s.modelsTable)
		b.WriteString(`.official_provider,`)
	} else {
		b.WriteString(`COALESCE(NULLIF(EXCLUDED.official_provider, ''), `)
		b.WriteString(s.modelsTable)
		b.WriteString(`.official_provider),`)
	}
	b.WriteString(`
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

// RenameProvider relocates every models_catalog row whose provider column equals
// oldProvider onto newProvider, in place. It is used by the upstream-providers
// editor when an operator renames a provider's name/identifier (e.g. "opencode"
// → "oc-openai"): catalog rows persist with provider = OwnedBy (set verbatim
// from the upstream name), so a rename must repoint the column or the old name
// lingers as a phantom entry in the Models Catalog / Provider Official picker.
//
// ownerBy mirrors how the catalog is seeded (Provider == OwnedBy), so the
// owned_by column is repointed in lockstep; official_provider is repointed only
// when it still carries the old provider string (i.e. the auto-sync placeholder
// that mirrors OwnedBy). Operator-edited official_provider values that differ
// from the old name are preserved untouched.
//
// PK-collision handling: a row keyed (id, newProvider) may already exist if the
// same model id is shared across two compat providers, or (more commonly) if the
// operator is renaming onto a name that already carried catalog rows from a
// prior sync. In that case the new-key row is treated as the source of truth —
// the colliding old-key rows are dropped because their ids are already
// represented under the target key — and the old-name rows that don't collide
// are simply repointed onto the new key. All of this runs in a single
// transaction so a partial rename never survives an error.
func (s *ModelsStore) RenameProvider(ctx context.Context, oldProvider, newProvider string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: models store not initialized")
	}
	oldProvider = strings.TrimSpace(oldProvider)
	newProvider = strings.TrimSpace(newProvider)
	if oldProvider == "" || newProvider == "" || oldProvider == newProvider {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres store: rename provider begin: %w", err)
	}
	defer func() {
		if tx == nil {
			return
		}
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			log.WithError(err).WithField("old", oldProvider).WithField("new", newProvider).
				Warn("postgres store: rename provider rollback failed")
		}
	}()

	// Drop old-key rows that already exist under the new key (same id), so the
	// subsequent UPDATE can't hit a (id, provider) PK violation. The new-key row
	// already represents that model id under the target provider.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE provider = $1 AND id IN (SELECT id FROM %s WHERE provider = $2)`,
		s.modelsTable, s.modelsTable,
	), oldProvider, newProvider); err != nil {
		return fmt.Errorf("postgres store: rename provider drop colliding: %w", err)
	}

	// Repoint the remaining old-key rows onto the new key.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET
			provider = $1,
			owned_by = COALESCE(NULLIF(owned_by, ''), $1),
			official_provider = CASE WHEN official_provider = $2 THEN $1 ELSE official_provider END,
			updated_at = NOW()
		WHERE provider = $2`,
		s.modelsTable,
	), newProvider, oldProvider); err != nil {
		return fmt.Errorf("postgres store: rename provider update: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("postgres store: rename provider commit: %w", err)
	}
	tx = nil
	return nil
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

// GlobalModelPatch carries the operator-settable attributes of a model that
// the "Global edit" surface can apply to every catalog row sharing the same
// model id (across providers). Pointer fields are nil when the caller did
// not ask to change that attribute — UpdateByID skips nil entries so an
// omitted field is never nullified. Pricing is intentionally NOT part of the
// patch because model_pricing is keyed by model id only (already global); it
// is handled separately by UsageStore.UpsertPricing.
type GlobalModelPatch struct {
	OfficialProvider    *string   // = official_provider
	DisplayName         *string   // = display_name
	Description         *string   // = description
	ContextLength       *int      // = context_length
	MaxCompletionTokens *int      // = max_completion_tokens
	InputTokenLimit     *int      // = input_token_limit
	OutputTokenLimit    *int      // = output_token_limit
	InputModalities     *[]string // = input_modalities (full replace)
	OutputModalities    *[]string // = output_modalities (full replace)
	// UserDefined is the "user-defined" flag per row. The auto-sync upsert
	// path sets it to false unconditionally, so a model id that exists under
	// multiple upstream providers can end up with one row marked user_defined
	// and the rest not — even after an "Apply globally" global edit, because
	// that flag was never part of this patch. Including it here lets the
	// dashboard's Global Model editor fan an explicit user_defined toggled
	// state out to every provider sharing the model id, so the operator no
	// longer has to open every (id, provider) row individually to align them.
	UserDefined *bool // = user_defined
}

// UpdateByID applies the non-nil fields of patch to every models_catalog row
// whose id matches case-insensitively (LOWER(id) = LOWER($1)). This is the
// store-level primitive backing the "Global Model management" feature: one
// operator edit fans out to all providers that serve the same model id, so
// pricing/attribute maintenance does not require editing each (id, provider)
// row individually. Returns the number of rows updated.
//
// Unlike the auto-sync upsert path, this update is authoritative: it targets
// all rows regardless of the user_defined flag, mirroring the dashboard's
// explicit PutModelEntry (protectUserDefined=false) semantics. The lowercase
// id match matches how live availability is keyed (see liveAvailableIDsSet).
func (s *ModelsStore) UpdateByID(ctx context.Context, id string, patch GlobalModelPatch) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: models store not initialized")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return 0, fmt.Errorf("postgres store: update by id requires non-empty id")
	}

	setParts := make([]string, 0, 10)
	args := make([]any, 0, 10)
	add := func(column string, value any) {
		args = append(args, value)
		setParts = append(setParts, fmt.Sprintf("%s = $%d", column, len(args)))
	}
	if patch.OfficialProvider != nil {
		add("official_provider", *patch.OfficialProvider)
	}
	if patch.DisplayName != nil {
		add("display_name", *patch.DisplayName)
	}
	if patch.Description != nil {
		add("description", *patch.Description)
	}
	if patch.ContextLength != nil {
		add("context_length", *patch.ContextLength)
	}
	if patch.MaxCompletionTokens != nil {
		add("max_completion_tokens", *patch.MaxCompletionTokens)
	}
	if patch.InputTokenLimit != nil {
		add("input_token_limit", *patch.InputTokenLimit)
	}
	if patch.OutputTokenLimit != nil {
		add("output_token_limit", *patch.OutputTokenLimit)
	}
	if patch.InputModalities != nil {
		add("input_modalities", marshalJSONBArray(*patch.InputModalities))
	}
	if patch.OutputModalities != nil {
		add("output_modalities", marshalJSONBArray(*patch.OutputModalities))
	}
	if patch.UserDefined != nil {
		add("user_defined", *patch.UserDefined)
	}
	if len(setParts) == 0 {
		// Nothing to change — no rows affected, no error.
		return 0, nil
	}
	setParts = append(setParts, "updated_at = NOW()")

	// Bind the id filter last so placeholder numbering stays contiguous.
	args = append(args, id)
	query := fmt.Sprintf(`UPDATE %s SET %s WHERE LOWER(id) = LOWER($%d)`,
		s.modelsTable, strings.Join(setParts, ", "), len(args))
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("postgres store: update by id: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("postgres store: update by id rows affected: %w", err)
	}
	return n, nil
}

// marshalJSONBArray renders a []string as a JSON array string suitable for a
// jsonb column. nil/empty yields an empty JSON array "[]" (matching the
// decodeStringArray round-trip) so an explicit global set of "no modalities"
// is stored rather than NULL.
func marshalJSONBArray(values []string) any {
	if len(values) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

// SelectByIDAllProviders returns every catalog row that shares the given model
// id (case-insensitive), ordered by provider. Used by the Global Model editor
// to preview which providers will be affected by a global edit and to seed the
// canonical attribute form from a representative row. Returns an empty (non-nil)
// slice when no row matches (the model id is unknown to the catalog).
func (s *ModelsStore) SelectByIDAllProviders(ctx context.Context, id string) ([]StoredModel, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: models store not initialized")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("postgres store: select by id requires non-empty id")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, provider, official_provider, object, created, owned_by, type, display_name,
		       name, version, description, input_token_limit, output_token_limit,
		       supported_generation_methods, context_length, max_completion_tokens,
		       supported_parameters, input_modalities, output_modalities,
		       supports_web_search, thinking_config, override_header, user_defined,
		       updated_at
		FROM %s WHERE LOWER(id) = LOWER($1)
		ORDER BY provider
	`, s.modelsTable), id)
	if err != nil {
		return nil, fmt.Errorf("postgres store: select by id all providers: %w", err)
	}
	defer rows.Close()
	out := make([]StoredModel, 0, 4)
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
			return nil, fmt.Errorf("postgres store: scan model row (by id): %w", err)
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

// GlobalModelRouteUpsert is the routing-only portion of a global model route.
// It carries the same fields as a Models Group ModelRoute (pinned providers +
// strategy + priorities) so the request-time routing decision applies the exact
// same intersect/order/stash mechanics for a Global Model as it does for a
// per-API-key Models Group route. Per-model caps (RPM/budget/discount) are
// policy concepts and are deliberately NOT included.
type GlobalModelRouteUpsert struct {
	Providers  []string
	Strategy   string
	Priorities []ProviderPriority
}

// Normalize returns a cleaned copy of the route: providers trimmed and
// de-duplicated (case-insensitive), strategy lowercased, and Priorities
// filtered to providers actually listed in Providers. This mirrors
// normalizeModelRoutes so persisted routing stays consistent.
func (r GlobalModelRouteUpsert) Normalize() GlobalModelRouteUpsert {
	providers := make([]string, 0, len(r.Providers))
	providerSet := make(map[string]struct{}, len(r.Providers))
	for _, p := range r.Providers {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		key := strings.ToLower(p)
		if _, dup := providerSet[key]; dup {
			continue
		}
		providerSet[key] = struct{}{}
		providers = append(providers, p)
	}
	strategy := strings.ToLower(strings.TrimSpace(r.Strategy))
	var priorities []ProviderPriority
	for _, pr := range r.Priorities {
		provider := strings.TrimSpace(pr.Provider)
		if provider == "" {
			continue
		}
		if _, ok := providerSet[strings.ToLower(provider)]; !ok {
			continue
		}
		priorities = append(priorities, ProviderPriority{Provider: provider, Priority: pr.Priority})
	}
	return GlobalModelRouteUpsert{Providers: providers, Strategy: strategy, Priorities: priorities}
}

// UpsertGlobalModelRoute persists a per-model-id global routing override and
// refreshes the in-memory route cache so the request-time routing decision
// reflects the change immediately. empty indicates the route should be cleared
// (deleted) rather than written.
func (s *ModelsStore) UpsertGlobalModelRoute(ctx context.Context, id string, route GlobalModelRouteUpsert, empty bool) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: models store not initialized")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("postgres store: global route upsert requires non-empty id")
	}
	route = route.Normalize()
	key := strings.ToLower(id)

	if empty || len(route.Providers) == 0 {
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(
			`DELETE FROM %s WHERE LOWER(id) = LOWER($1)`, s.routingTable,
		), id); err != nil {
			return fmt.Errorf("postgres store: delete global route: %w", err)
		}
		s.setRouteCache(key, nil)
		return nil
	}

	providersJSON, err := json.Marshal(route.Providers)
	if err != nil {
		return fmt.Errorf("postgres store: marshal global route providers: %w", err)
	}
	var prioritiesJSON []byte
	if len(route.Priorities) > 0 {
		prioritiesJSON, err = json.Marshal(route.Priorities)
		if err != nil {
			return fmt.Errorf("postgres store: marshal global route priorities: %w", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, providers, strategy, priorities, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (id) DO UPDATE SET
			providers = EXCLUDED.providers,
			strategy = EXCLUDED.strategy,
			priorities = EXCLUDED.priorities,
			updated_at = NOW()
	`, s.routingTable), id, string(providersJSON), route.Strategy, string(prioritiesJSON)); err != nil {
		return fmt.Errorf("postgres store: upsert global route: %w", err)
	}

	s.setRouteCache(key, &ModelRoute{
		Model:      id,
		Providers:  route.Providers,
		Strategy:   route.Strategy,
		Priorities: route.Priorities,
	})
	return nil
}

// GlobalModelRoute returns the persisted global routing override for model id,
// or nil when none is set. Results are cached in memory (keyed by lowercased
// model id), so request-time reads incur at most one DB lookup per model. An
// error on a cache-miss DB read returns nil so the routing decision degrades to
// the default registry providers rather than failing the request.
func (s *ModelsStore) GlobalModelRoute(ctx context.Context, id string) *ModelRoute {
	if s == nil || s.db == nil {
		return nil
	}
	key := strings.ToLower(strings.TrimSpace(id))
	if key == "" {
		return nil
	}
	if r, ok := s.getRouteCache(key); ok {
		// Copy so callers cannot mutate the cached route.
		if r == nil {
			return nil
		}
		cp := *r
		return &cp
	}

	var providersRaw, prioritiesRaw []byte
	var strategy string
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT providers, strategy, priorities FROM %s WHERE LOWER(id) = LOWER($1)`, s.routingTable,
	), id).Scan(&providersRaw, &strategy, &prioritiesRaw)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.WithError(err).Warn("postgres store: read global route: " + id)
		}
		s.setRouteCache(key, nil)
		return nil
	}
	var providers []string
	_ = json.Unmarshal(providersRaw, &providers)
	var priorities []ProviderPriority
	_ = json.Unmarshal(prioritiesRaw, &priorities)
	if len(providers) == 0 {
		s.setRouteCache(key, nil)
		return nil
	}
	route := &ModelRoute{Model: id, Providers: providers, Strategy: strategy, Priorities: priorities}
	cp := *route
	s.setRouteCache(key, route)
	return &cp
}

func (s *ModelsStore) setRouteCache(key string, route *ModelRoute) {
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	s.routeCache[key] = route
}

// getRouteCache is a cache-lookup helper: false (with empty second value)
// means not cached yet; true means cached (value may be nil = negative result).
func (s *ModelsStore) getRouteCache(key string) (*ModelRoute, bool) {
	s.routeMu.RLock()
	defer s.routeMu.RUnlock()
	r, ok := s.routeCache[key]
	return r, ok
}
