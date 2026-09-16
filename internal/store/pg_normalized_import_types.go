package store

import "fmt"

// NormalizedResourcePlan is the pure output of the configsnapshot planner.
// It lives in the store package so the transactional apply path can consume
// it without creating an import cycle: configsnapshot depends on store, so
// store must not depend on configsnapshot.
type NormalizedResourcePlan struct {
	Providers []UpstreamProvider
	APIKeys   []APIKey
	Report    ImportReport
}

// ImportReport is mutable, so the planner writes outcomes incrementally and
// the apply path updates Committed/RolledBack flags after a successful
// commit or a transactional failure.
type ImportReport struct {
	Counts      map[string]map[string]int
	Errors      map[string][]string
	Unsupported map[string][]string
	Planned     bool
	Committed   bool
	RolledBack  bool
}

// Created/Updated/Unchanged bump the per-kind action counter. The planner
// records Created/Updated and the apply path records Unchanged when an
// existing row already matches the canonical plan.
func (r *ImportReport) Created(kind string) int   { return r.bump(kind, "created") }
func (r *ImportReport) Updated(kind string) int   { return r.bump(kind, "updated") }
func (r *ImportReport) Unchanged(kind string) int { return r.bump(kind, "unchanged") }

func (r *ImportReport) bump(kind, action string) int {
	if r == nil {
		return 0
	}
	if r.Counts == nil {
		r.Counts = map[string]map[string]int{}
	}
	m, ok := r.Counts[kind]
	if !ok {
		m = map[string]int{}
		r.Counts[kind] = m
	}
	m[action]++
	return m[action]
}

// AddError records a per-resource error with kind + identity context.
// Plaintext secrets must never appear here.
func (r *ImportReport) AddError(kind, identity string, err error) {
	if r == nil || err == nil {
		return
	}
	if r.Errors == nil {
		r.Errors = map[string][]string{}
	}
	key := kind
	if identity != "" {
		key = kind + ":" + identity
	}
	r.Errors[key] = append(r.Errors[key], err.Error())
}

// MarkUnsupported records a config field the importer preserves in
// ExtraConfig because no normalized column exists.
func (r *ImportReport) MarkUnsupported(kind, field, reason string) {
	if r == nil {
		return
	}
	if r.Unsupported == nil {
		r.Unsupported = map[string][]string{}
	}
	r.Unsupported[kind] = append(r.Unsupported[kind], fmt.Sprintf("%s=%s (%s)", field, reason, kind))
}

// Bump increments and returns an arbitrary per-kind action counter. Used by
// the planner for cases without a dedicated helper, e.g. the
// "internal_users/not_applicable" marker for sections with no YAML source.
func (r *ImportReport) Bump(kind, action string) int {
	return r.bump(kind, action)
}
