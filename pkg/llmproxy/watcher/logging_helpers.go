package watcher

import "fmt"

func summarizeStaticCredentialClients(gemini, vertex, claude, codex, openAICompat int) int {
	return gemini + vertex + claude + codex + openAICompat
}

func clientReloadSummary(totalClients, authFileCount, staticCredentialClients int) string {
	return fmt.Sprintf(
		"full client load complete - %d clients (%d auth files + %d static credential clients)",
		totalClients,
		authFileCount,
		staticCredentialClients,
	)
}

func redactedConfigChangeLogLines(details []string) []string {
	lines := make([]string, 0, len(details))
	for i := range details {
		lines = append(lines, fmt.Sprintf("  change[%d] recorded (redacted)", i+1))
	}
	return lines
}
