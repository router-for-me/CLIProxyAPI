package executor

import executorhelps "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/executor/helps"

func isValidUserID(userID string) bool {
	return executorhelps.IsValidUserID(userID)
}
