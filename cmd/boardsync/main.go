package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	targetCount = 2000
)

var repos = []string{
	"router-for-me/CLIProxyAPIPlus",
	"router-for-me/CLIProxyAPI",
}

type sourceItem struct {
	Kind      string   `json:"kind"`
	Repo      string   `json:"repo"`
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	URL       string   `json:"url"`
	Labels    []string `json:"labels"`
	Comments  int      `json:"comments"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
	Body      string   `json:"body"`
}

type boardItem struct {
	ID                  string `json:"id"`
	Theme               string `json:"theme"`
	Title               string `json:"title"`
	Priority            string `json:"priority"`
	Effort              string `json:"effort"`
	Wave                string `json:"wave"`
	Status              string `json:"status"`
	ImplementationReady string `json:"implementation_ready"`
	SourceKind          string `json:"source_kind"`
	SourceRepo          string `json:"source_repo"`
	SourceRef           string `json:"source_ref"`
	SourceURL           string `json:"source_url"`
	ImplementationNote  string `json:"implementation_note"`
}

type boardJSON struct {
	Stats  map[string]int            `json:"stats"`
	Counts map[string]map[string]int `json:"counts"`
	Items  []boardItem               `json:"items"`
}

type discussionNode struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	Closed    bool   `json:"closed"`
	BodyText  string `json:"bodyText"`
	Category  struct {
		Name string `json:"name"`
	} `json:"category"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	Comments struct {
		TotalCount int `json:"totalCount"`
	} `json:"comments"`
}

func main() {
	root, err := os.Getwd()
	if err != nil {
		fail(err)
	}

	tmpDir := filepath.Join(root, "tmp", "gh_board")
	planDir := filepath.Join(root, "docs", "planning")
	must(os.MkdirAll(tmpDir, 0o755))
	must(os.MkdirAll(planDir, 0o755))

	for _, repo := range repos {
		must(fetchRepoSnapshots(tmpDir, repo))
	}

	sources, stats, err := loadSources(tmpDir)
	if err != nil {
		fail(err)
	}

	board := buildBoard(sources)
	sortBoard(board)

	jsonObj := boardJSON{
		Stats:  stats,
		Counts: summarizeCounts(board),
		Items:  board,
	}

	const base = "CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22"
	boardJSONPath := filepath.Join(planDir, base+".json")
	boardCSVPath := filepath.Join(planDir, base+".csv")
	boardMDPath := filepath.Join(planDir, base+".md")
	importCSVPath := filepath.Join(planDir, "GITHUB_PROJECT_IMPORT_CLIPROXYAPI_2000_2026-02-22.csv")

	must(writeBoardJSON(boardJSONPath, jsonObj))
	must(writeBoardCSV(boardCSVPath, board))
	must(writeBoardMarkdown(boardMDPath, board, jsonObj))
	must(writeProjectImportCSV(importCSVPath, board))

	fmt.Println("board sync complete")
	fmt.Println(boardJSONPath)
	fmt.Println(boardCSVPath)
	fmt.Println(boardMDPath)
	fmt.Println(importCSVPath)
	fmt.Printf("items=%d\n", len(board))
}

func fetchRepoSnapshots(tmpDir, repo string) error {
	base := strings.ReplaceAll(repo, "/", "_")
	if err := ghToFile([]string{"api", "--paginate", "repos/" + repo + "/issues?state=all&per_page=100"}, filepath.Join(tmpDir, base+"_issues_prs.json")); err != nil {
		return err
	}
	if err := ghToFile([]string{"api", "--paginate", "repos/" + repo + "/pulls?state=all&per_page=100"}, filepath.Join(tmpDir, base+"_pulls.json")); err != nil {
		return err
	}
	discussions, err := fetchDiscussions(repo)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(discussions, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(tmpDir, base+"_discussions_graphql.json"), b, 0o644)
}

func ghToFile(args []string, path string) error {
	out, err := run("gh", args...)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

func fetchDiscussions(repo string) ([]discussionNode, error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repo: %s", repo)
	}
	owner, name := parts[0], parts[1]
	cursor := ""
	var all []discussionNode

	for {
		q := `query($owner:String!,$repo:String!,$first:Int!,$after:String){
		  repository(owner:$owner,name:$repo){
		    discussions(first:$first,after:$after,orderBy:{field:UPDATED_AT,direction:DESC}){
		      nodes{
		        number title url createdAt updatedAt closed bodyText
		        category{name}
		        author{login}
		        comments{totalCount}
		      }
		      pageInfo{hasNextPage endCursor}
		    }
		  }
		}`
		args := []string{"api", "graphql", "-f", "owner=" + owner, "-f", "repo=" + name, "-F", "first=50", "-f", "query=" + q}
		if cursor != "" {
			args = append(args, "-f", "after="+cursor)
		}
		out, err := run("gh", args...)
		if err != nil {
			// repo may not have discussions enabled; treat as empty
			return all, nil
		}
		var resp struct {
			Data struct {
				Repository struct {
					Discussions struct {
						Nodes    []discussionNode `json:"nodes"`
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"discussions"`
				} `json:"repository"`
			} `json:"data"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Data.Repository.Discussions.Nodes...)
		if !resp.Data.Repository.Discussions.PageInfo.HasNextPage {
			break
		}
		cursor = resp.Data.Repository.Discussions.PageInfo.EndCursor
		if cursor == "" {
			break
		}
	}
	return all, nil
}

func loadSources(tmpDir string) ([]sourceItem, map[string]int, error) {
	var out []sourceItem
	stats := map[string]int{
		"sources_total_unique": 0,
		"issues_plus":          0,
		"issues_core":          0,
		"prs_plus":             0,
		"prs_core":             0,
		"discussions_plus":     0,
		"discussions_core":     0,
	}

	for _, repo := range repos {
		base := strings.ReplaceAll(repo, "/", "_")

		issuesPath := filepath.Join(tmpDir, base+"_issues_prs.json")
		pullsPath := filepath.Join(tmpDir, base+"_pulls.json")
		discussionsPath := filepath.Join(tmpDir, base+"_discussions_graphql.json")

		var issues []map[string]any
		if err := readJSON(issuesPath, &issues); err != nil {
			return nil, nil, err
		}
		for _, it := range issues {
			if _, isPR := it["pull_request"]; isPR {
				continue
			}
			s := sourceItem{
				Kind:      "issue",
				Repo:      repo,
				Number:    intFromAny(it["number"]),
				Title:     strFromAny(it["title"]),
				State:     strFromAny(it["state"]),
				URL:       strFromAny(it["html_url"]),
				Labels:    labelsFromAny(it["labels"]),
				Comments:  intFromAny(it["comments"]),
				CreatedAt: strFromAny(it["created_at"]),
				UpdatedAt: strFromAny(it["updated_at"]),
				Body:      shrink(strFromAny(it["body"]), 1200),
			}
			out = append(out, s)
			if strings.HasSuffix(repo, "CLIProxyAPIPlus") {
				stats["issues_plus"]++
			} else {
				stats["issues_core"]++
			}
		}

		var pulls []map[string]any
		if err := readJSON(pullsPath, &pulls); err != nil {
			return nil, nil, err
		}
		for _, it := range pulls {
			s := sourceItem{
				Kind:      "pr",
				Repo:      repo,
				Number:    intFromAny(it["number"]),
				Title:     strFromAny(it["title"]),
				State:     strFromAny(it["state"]),
				URL:       strFromAny(it["html_url"]),
				Labels:    labelsFromAny(it["labels"]),
				Comments:  intFromAny(it["comments"]),
				CreatedAt: strFromAny(it["created_at"]),
				UpdatedAt: strFromAny(it["updated_at"]),
				Body:      shrink(strFromAny(it["body"]), 1200),
			}
			out = append(out, s)
			if strings.HasSuffix(repo, "CLIProxyAPIPlus") {
				stats["prs_plus"]++
			} else {
				stats["prs_core"]++
			}
		}

		var discussions []discussionNode
		if err := readJSON(discussionsPath, &discussions); err != nil {
			return nil, nil, err
		}
		for _, d := range discussions {
			s := sourceItem{
				Kind:      "discussion",
				Repo:      repo,
				Number:    d.Number,
				Title:     d.Title,
				State:     ternary(d.Closed, "closed", "open"),
				URL:       d.URL,
				Labels:    []string{d.Category.Name},
				Comments:  d.Comments.TotalCount,
				CreatedAt: d.CreatedAt,
				UpdatedAt: d.UpdatedAt,
				Body:      shrink(d.BodyText, 1200),
			}
			out = append(out, s)
			if strings.HasSuffix(repo, "CLIProxyAPIPlus") {
				stats["discussions_plus"]++
			} else {
				stats["discussions_core"]++
			}
		}
	}

	seen := map[string]bool{}
	dedup := make([]sourceItem, 0, len(out))
	for _, s := range out {
		if s.URL == "" || seen[s.URL] {
			continue
		}
		seen[s.URL] = true
		dedup = append(dedup, s)
	}
	stats["sources_total_unique"] = len(dedup)
	return dedup, stats, nil
}

func buildBoard(sources []sourceItem) []boardItem {
	seed := []boardItem{
		newSeed("CP2K-0001", "platform-architecture", "Port thegent proxy lifecycle/install/login/model-management flows into first-class cliproxy Go CLI commands.", "P1", "L", "wave-1"),
		newSeed("CP2K-0002", "integration-api-bindings", "Define a non-subprocess integration contract: Go bindings first, HTTP API fallback, versioned capability negotiation.", "P1", "L", "wave-1"),
		newSeed("CP2K-0003", "dev-runtime-refresh", "Add process-compose dev profile with HMR-style reload, config watcher, and explicit `cliproxy refresh` command.", "P1", "M", "wave-1"),
		newSeed("CP2K-0004", "docs-quickstarts", "Publish provider-specific 5-minute quickstarts with auth + model selection + sanity-check commands.", "P1", "M", "wave-1"),
		newSeed("CP2K-0005", "docs-quickstarts", "Add troubleshooting matrix for auth, model mapping, thinking normalization, stream parsing, and retry semantics.", "P1", "M", "wave-1"),
		newSeed("CP2K-0006", "cli-ux-dx", "Ship interactive setup wizard and `doctor --fix` with machine-readable JSON output and deterministic remediation.", "P1", "M", "wave-1"),
		newSeed("CP2K-0007", "testing-and-quality", "Add cross-provider OpenAI Responses/Chat Completions conformance test suite with golden fixtures.", "P1", "L", "wave-1"),
		newSeed("CP2K-0008", "testing-and-quality", "Add dedicated reasoning controls tests (`variant`, `reasoning_effort`, `reasoning.effort`, suffix forms).", "P1", "M", "wave-1"),
		newSeed("CP2K-0009", "project-frontmatter", "Rewrite project frontmatter/readme with architecture, compatibility matrix, provider guides, support policy, and release channels.", "P2", "M", "wave-1"),
		newSeed("CP2K-0010", "install-and-ops", "Improve release and install UX with unified install flow, binary verification, and platform post-install checks.", "P2", "M", "wave-1"),
	}

	templates := []string{
		`Follow up "%s" by closing compatibility gaps and locking in regression coverage.`,
		`Harden "%s" with stricter validation, safer defaults, and explicit fallback semantics.`,
		`Operationalize "%s" with observability, runbook updates, and deployment safeguards.`,
		`Generalize "%s" into provider-agnostic translation/utilities to reduce duplicate logic.`,
		`Improve CLI UX around "%s" with clearer commands, flags, and immediate validation feedback.`,
		`Extend docs for "%s" with quickstart snippets and troubleshooting decision trees.`,
		`Add robust stream/non-stream parity tests for "%s" across supported providers.`,
		`Refactor internals touched by "%s" to reduce coupling and improve maintainability.`,
		`Prepare safe rollout for "%s" via flags, migration docs, and backward-compat tests.`,
		`Standardize naming/metadata affected by "%s" across both repos and docs.`,
	}

	actions := []string{
		"Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.",
		"Add failing-before/failing-after regression tests and update golden fixtures for each supported provider.",
		"Improve error diagnostics and add actionable remediation text in CLI and docs.",
		"Refactor translation layer to isolate provider transform logic from transport concerns.",
		"Instrument structured logs/metrics around request normalize->translate->dispatch lifecycle.",
		"Add staged rollout controls (feature flags) with safe defaults and migration notes.",
		"Harden edge-case parsing for stream and non-stream payload variants.",
		"Benchmark p50/p95 latency and memory; reject regressions in CI quality gate.",
		"Expand quickstart and troubleshooting docs with copy-paste examples and expected outputs.",
		"Add contract tests for malformed payloads, missing fields, and legacy/new mixed parameters.",
	}

	board := make([]boardItem, 0, targetCount)
	board = append(board, seed...)

	for i := len(seed) + 1; len(board) < targetCount; i++ {
		src := sources[(i-1)%len(sources)]
		title := clean(src.Title)
		if title == "" {
			title = fmt.Sprintf("%s #%d", src.Kind, src.Number)
		}

		theme := pickTheme(title + " " + src.Body)
		itemTitle := fmt.Sprintf(templates[(i-1)%len(templates)], title)
		priority := pickPriority(src)
		effort := pickEffort(src)

		switch {
		case i%17 == 0:
			theme = "docs-quickstarts"
			itemTitle = fmt.Sprintf(`Create or refresh provider quickstart derived from "%s" with setup/auth/model/sanity-check flow.`, title)
			priority = "P1"
		case i%19 == 0:
			theme = "go-cli-extraction"
			itemTitle = fmt.Sprintf(`Port relevant thegent-managed behavior implied by "%s" into cliproxy Go CLI commands and interactive setup.`, title)
			priority, effort = "P1", "M"
		case i%23 == 0:
			theme = "integration-api-bindings"
			itemTitle = fmt.Sprintf(`Design non-subprocess integration contract related to "%s" with Go bindings primary and API fallback.`, title)
			priority, effort = "P1", "M"
		case i%29 == 0:
			theme = "dev-runtime-refresh"
			itemTitle = fmt.Sprintf(`Add process-compose/HMR refresh workflow linked to "%s" for deterministic local runtime reload.`, title)
			priority, effort = "P1", "M"
		}

		board = append(board, boardItem{
			ID:                  fmt.Sprintf("CP2K-%04d", i),
			Theme:               theme,
			Title:               itemTitle,
			Priority:            priority,
			Effort:              effort,
			Wave:                pickWave(priority, effort),
			Status:              "proposed",
			ImplementationReady: "yes",
			SourceKind:          src.Kind,
			SourceRepo:          src.Repo,
			SourceRef:           fmt.Sprintf("%s#%d", src.Kind, src.Number),
			SourceURL:           src.URL,
			ImplementationNote:  actions[(i-1)%len(actions)],
		})
	}

	return board
}

func sortBoard(board []boardItem) {
	pr := map[string]int{"P1": 0, "P2": 1, "P3": 2}
	wr := map[string]int{"wave-1": 0, "wave-2": 1, "wave-3": 2}
	er := map[string]int{"S": 0, "M": 1, "L": 2}
	sort.SliceStable(board, func(i, j int) bool {
		a, b := board[i], board[j]
		if pr[a.Priority] != pr[b.Priority] {
			return pr[a.Priority] < pr[b.Priority]
		}
		if wr[a.Wave] != wr[b.Wave] {
			return wr[a.Wave] < wr[b.Wave]
		}
		if er[a.Effort] != er[b.Effort] {
			return er[a.Effort] < er[b.Effort]
		}
		return a.ID < b.ID
	})
}

func summarizeCounts(board []boardItem) map[string]map[string]int {
	out := map[string]map[string]int{
		"priority": {},
		"wave":     {},
		"effort":   {},
		"theme":    {},
	}
	for _, b := range board {
		out["priority"][b.Priority]++
		out["wave"][b.Wave]++
		out["effort"][b.Effort]++
		out["theme"][b.Theme]++
	}
	return out
}

func writeBoardJSON(path string, data boardJSON) error {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func writeBoardCSV(path string, board []boardItem) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"id", "theme", "title", "priority", "effort", "wave", "status", "implementation_ready", "source_kind", "source_repo", "source_ref", "source_url", "implementation_note"}); err != nil {
		return err
	}
	for _, b := range board {
		if err := w.Write([]string{b.ID, b.Theme, b.Title, b.Priority, b.Effort, b.Wave, b.Status, b.ImplementationReady, b.SourceKind, b.SourceRepo, b.SourceRef, b.SourceURL, b.ImplementationNote}); err != nil {
			return err
		}
	}
	return nil
}

func writeProjectImportCSV(path string, board []boardItem) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"Title", "Body", "Status", "Priority", "Wave", "Effort", "Theme", "Implementation Ready", "Source Kind", "Source Repo", "Source Ref", "Source URL", "Labels", "Board ID"}); err != nil {
		return err
	}
	for _, b := range board {
		body := fmt.Sprintf("Execution item %s | Source: %s %s | Source URL: %s | Implementation note: %s | Tracking rule: keep source->solution mapping and update Status as work progresses.", b.ID, b.SourceRepo, b.SourceRef, b.SourceURL, b.ImplementationNote)
		labels := strings.Join([]string{
			"board-2000",
			"theme:" + b.Theme,
			"prio:" + strings.ToLower(b.Priority),
			"wave:" + b.Wave,
			"effort:" + strings.ToLower(b.Effort),
			"kind:" + b.SourceKind,
		}, ",")
		if err := w.Write([]string{b.Title, body, b.Status, b.Priority, b.Wave, b.Effort, b.Theme, b.ImplementationReady, b.SourceKind, b.SourceRepo, b.SourceRef, b.SourceURL, labels, b.ID}); err != nil {
			return err
		}
	}
	return nil
}

func writeBoardMarkdown(path string, board []boardItem, bj boardJSON) error {
	var buf bytes.Buffer
	now := time.Now().Format("2006-01-02")
	buf.WriteString("# CLIProxyAPI Ecosystem 2000-Item Execution Board\n\n")
	fmt.Fprintf(&buf, "- Generated: %s\n", now)
	buf.WriteString("- Scope: `router-for-me/CLIProxyAPIPlus` + `router-for-me/CLIProxyAPI` Issues, PRs, Discussions\n")
	buf.WriteString("- Objective: Implementation-ready backlog (up to 2000), including CLI extraction, bindings/API integration, docs quickstarts, and dev-runtime refresh\n\n")
	buf.WriteString("## Coverage\n")
	keys := []string{"generated_items", "sources_total_unique", "issues_plus", "issues_core", "prs_plus", "prs_core", "discussions_plus", "discussions_core"}
	bj.Stats["generated_items"] = len(board)
	for _, k := range keys {
		fmt.Fprintf(&buf, "- %s: %d\n", k, bj.Stats[k])
	}
	buf.WriteString("\n## Distribution\n")
	for _, sec := range []string{"priority", "wave", "effort", "theme"} {
		fmt.Fprintf(&buf, "### %s\n", cases.Title(language.Und).String(sec))
		type kv struct {
			K string
			V int
		}
		var arr []kv
		for k, v := range bj.Counts[sec] {
			arr = append(arr, kv{K: k, V: v})
		}
		sort.Slice(arr, func(i, j int) bool {
			if arr[i].V != arr[j].V {
				return arr[i].V > arr[j].V
			}
			return arr[i].K < arr[j].K
		})
		for _, p := range arr {
			fmt.Fprintf(&buf, "- %s: %d\n", p.K, p.V)
		}
		buf.WriteString("\n")
	}

	buf.WriteString("## Top 250 (Execution Order)\n\n")
	limit := 250
	if len(board) < limit {
		limit = len(board)
	}
	for _, b := range board[:limit] {
		fmt.Fprintf(&buf, "### [%s] %s\n", b.ID, b.Title)
		fmt.Fprintf(&buf, "- Priority: %s\n", b.Priority)
		fmt.Fprintf(&buf, "- Wave: %s\n", b.Wave)
		fmt.Fprintf(&buf, "- Effort: %s\n", b.Effort)
		fmt.Fprintf(&buf, "- Theme: %s\n", b.Theme)
		fmt.Fprintf(&buf, "- Source: %s %s\n", b.SourceRepo, b.SourceRef)
		if b.SourceURL != "" {
			fmt.Fprintf(&buf, "- Source URL: %s\n", b.SourceURL)
		}
		fmt.Fprintf(&buf, "- Implementation note: %s\n\n", b.ImplementationNote)
	}
	buf.WriteString("## Full 2000 Items\n")
	buf.WriteString("- Use the CSV/JSON artifacts for full import and sorting.\n")

	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func newSeed(id, theme, title, priority, effort, wave string) boardItem {
	return boardItem{
		ID:                  id,
		Theme:               theme,
		Title:               title,
		Priority:            priority,
		Effort:              effort,
		Wave:                wave,
		Status:              "proposed",
		ImplementationReady: "yes",
		SourceKind:          "strategy",
		SourceRepo:          "cross-repo",
		SourceRef:           "synthesis",
		SourceURL:           "",
		ImplementationNote:  "Implement compatibility-preserving normalization path with explicit fallback behavior and telemetry.",
	}
}

func pickTheme(text string) string {
	t := strings.ToLower(text)
	cases := []struct {
		theme string
		keys  []string
	}{
		{"thinking-and-reasoning", []string{"reasoning", "thinking", "effort", "variant", "budget", "token"}},
		{"responses-and-chat-compat", []string{"responses", "chat/completions", "translator", "message", "tool call", "response_format"}},
		{"provider-model-registry", []string{"model", "registry", "alias", "metadata", "provider"}},
		{"oauth-and-authentication", []string{"oauth", "login", "auth", "token exchange", "credential"}},
		{"websocket-and-streaming", []string{"websocket", "sse", "stream", "delta", "chunk"}},
		{"error-handling-retries", []string{"error", "retry", "429", "cooldown", "timeout", "backoff", "limit"}},
		{"docs-quickstarts", []string{"readme", "docs", "quick start", "guide", "example", "tutorial"}},
		{"install-and-ops", []string{"docker", "compose", "install", "build", "binary", "release", "ops"}},
		{"cli-ux-dx", []string{"cli", "command", "flag", "wizard", "ux", "dx", "tui", "interactive"}},
		{"testing-and-quality", []string{"test", "ci", "coverage", "lint", "benchmark", "contract"}},
	}
	for _, c := range cases {
		for _, k := range c.keys {
			if strings.Contains(t, k) {
				return c.theme
			}
		}
	}
	return "general-polish"
}

func pickPriority(src sourceItem) string {
	t := strings.ToLower(src.Title + " " + src.Body)
	if containsAny(t, []string{"oauth", "login", "auth", "translator", "responses", "stream", "reasoning", "token exchange", "critical", "security", "429"}) {
		return "P1"
	}
	if containsAny(t, []string{"docs", "readme", "guide", "example", "polish", "ux", "dx"}) {
		return "P3"
	}
	return "P2"
}

func pickEffort(src sourceItem) string {
	switch src.Kind {
	case "discussion":
		return "S"
	case "pr":
		return "M"
	default:
		return "S"
	}
}

func pickWave(priority, effort string) string {
	if priority == "P1" && (effort == "S" || effort == "M") {
		return "wave-1"
	}
	if priority == "P1" && effort == "L" {
		return "wave-2"
	}
	if priority == "P2" {
		return "wave-2"
	}
	return "wave-3"
}

func clean(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	return strings.Join(strings.Fields(s), " ")
}

func containsAny(s string, tokens []string) bool {
	for _, t := range tokens {
		if strings.Contains(s, t) {
			return true
		}
	}
	return false
}

func shrink(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func readJSON(path string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func labelsFromAny(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		name := strFromAny(m["name"])
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func intFromAny(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case json.Number:
		i, _ := t.Int64()
		return int(i)
	default:
		return 0
	}
}

func strFromAny(v any) string {
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

func run(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("command failed: %s %s: %w; output=%s", name, strings.Join(args, " "), err, string(out))
	}
	return out, nil
}

func must(err error) {
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	if err == nil {
		err = errors.New("unknown error")
	}
	fmt.Fprintln(os.Stderr, err.Error())
	os.Exit(1)
}
