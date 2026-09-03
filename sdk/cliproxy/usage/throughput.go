package usage

import "time"

// TokensPerSecond is output-token generation throughput excluding TTFT.
// Returns 0 when TTFT is unknown so callers omit the field instead of using
// tokens / total_latency (which includes queueing and first-token wait).
func TokensPerSecond(outputTokens int64, latency, ttft time.Duration) float64 {
	if outputTokens <= 0 || latency <= 0 || ttft <= 0 {
		return 0
	}
	generation := latency - ttft
	if generation <= 0 {
		return 0
	}
	return float64(outputTokens) / generation.Seconds()
}
