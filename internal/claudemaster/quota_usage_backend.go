package claudemaster

import "net/http"

func parseBackendWeeklyQuotaHeaders(headers http.Header) (backendWeeklyQuota, bool) {
	quota, known := ParseClaudeWeeklyQuotaHeaders(headers)
	if !known {
		return backendWeeklyQuota{}, false
	}
	return backendWeeklyQuota{known: true, used: quota.UsedFraction, resetsAt: quota.ResetsAt}, true
}
