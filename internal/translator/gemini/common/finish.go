package common

func IsTerminalFinishReason(reason string) bool {
	return reason != "" && reason != "FINISH_REASON_UNSPECIFIED" && reason != "UNKNOWN"
}
