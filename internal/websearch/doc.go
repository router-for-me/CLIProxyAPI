// Package websearch runs web queries through a configured chain of search
// providers and returns LLM-formatted answers with sources and citations.
//
// It is modeled after the Oh My Pi (OMP) web_search tool: Google-style query
// directives are parsed once, providers are tried sequentially until one
// returns renderable content, results are leniently post-filtered against
// query constraints, and output is rendered with formatForLLM semantics.
//
// The proxy uses this package to give every model a working web_search tool
// even when the upstream provider has no native search support.
package websearch
