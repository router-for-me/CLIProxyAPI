package websearch

import (
	"fmt"
	"strings"
)

// formatForLLM limits mirror OMP so downstream prompts stay bounded.
const (
	maxSnippetChars = 240
	maxQueriesShown = 3
	maxQueryChars   = 120
)

// FormatForLLM renders a search response as one model-facing text block:
// relaxation notes first, then answer, sources, citations, related
// questions, and the executed search queries.
func FormatForLLM(response SearchResponse) string {
	var out strings.Builder
	for _, note := range response.Notes {
		note = strings.TrimSpace(note)
		if note == "" {
			continue
		}
		out.WriteString(note)
		out.WriteString("\n")
	}
	answer := strings.TrimSpace(response.Answer)
	if answer != "" {
		out.WriteString(answer)
		out.WriteString("\n")
	}
	if len(response.Sources) > 0 {
		if answer != "" {
			fmt.Fprintf(&out, "\n## Sources (%d)\n", len(response.Sources))
		}
		for i := range response.Sources {
			source := response.Sources[i]
			title := strings.TrimSpace(source.Title)
			if title == "" {
				title = source.URL
			}
			header := fmt.Sprintf("[%d] %s", i+1, title)
			if age := strings.TrimSpace(source.Published); age != "" {
				header += " (" + age + ")"
			}
			out.WriteString(header)
			out.WriteString("\n")
			out.WriteString("    ")
			out.WriteString(strings.TrimSpace(source.URL))
			out.WriteString("\n")
			if snippet := strings.TrimSpace(source.Snippet); snippet != "" {
				out.WriteString("    ")
				out.WriteString(truncateChars(snippet, maxSnippetChars))
				out.WriteString("\n")
			}
		}
	}
	if len(response.Citations) > 0 {
		out.WriteString("\n## Citations\n")
		for i := range response.Citations {
			citation := response.Citations[i]
			fmt.Fprintf(&out, "[%d] %s\n", i+1, strings.TrimSpace(citation.URL))
			if title := strings.TrimSpace(citation.Title); title != "" {
				out.WriteString("    ")
				out.WriteString(title)
				out.WriteString("\n")
			}
			if text := strings.TrimSpace(citation.CitedText); text != "" {
				out.WriteString("    ")
				out.WriteString(truncateChars(text, maxSnippetChars))
				out.WriteString("\n")
			}
		}
	}
	if len(response.Related) > 0 {
		out.WriteString("\n## Related\n")
		for _, question := range response.Related {
			question = strings.TrimSpace(question)
			if question == "" {
				continue
			}
			out.WriteString("- ")
			out.WriteString(question)
			out.WriteString("\n")
		}
	}
	var queries []string
	for _, query := range response.SearchQueries {
		if strings.TrimSpace(query) != "" {
			queries = append(queries, query)
		}
		if len(queries) >= maxQueriesShown {
			break
		}
	}
	if len(queries) > 0 {
		shown := make([]string, 0, len(queries))
		for _, query := range queries {
			shown = append(shown, truncateChars(strings.TrimSpace(query), maxQueryChars))
		}
		out.WriteString("\nSearch queries: ")
		out.WriteString(strings.Join(shown, " | "))
		out.WriteString("\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

func truncateChars(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}
