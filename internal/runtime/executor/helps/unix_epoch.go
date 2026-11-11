package helps

import "time"

// UnixSecondsOrMilli interprets raw as Unix seconds, or as milliseconds when
// the value has millisecond precision (>1e12). Same heuristic as credential
// expiry normalisation; kept here so executors do not import auth internals.
func UnixSecondsOrMilli(raw int64) time.Time {
	if raw <= 0 {
		return time.Time{}
	}
	if raw > 1_000_000_000_000 {
		return time.UnixMilli(raw)
	}
	return time.Unix(raw, 0)
}
