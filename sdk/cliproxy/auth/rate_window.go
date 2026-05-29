package auth

import "time"

// rateWindowSeconds is the trailing window length used for per-account RPM/TPM
// accounting. One bucket per second gives a 60s sliding window.
const rateWindowSeconds = 60

// rateBucket accumulates requests and tokens observed during a single wall-clock
// second. The sec field guards against stale reuse when the ring wraps.
type rateBucket struct {
	sec  int64
	reqs int64
	toks int64
}

// rateWindow tracks per-auth requests and tokens over a trailing rateWindowSeconds
// window, plus the count of in-flight requests for concurrency limiting.
//
// rateWindow is intentionally NOT internally synchronized: every access must be
// performed while holding the owning Manager's mutex, mirroring the existing
// recentRequestRing pattern. This keeps Auth.Clone() (a by-value copy) free of
// lock-copy hazards.
type rateWindow struct {
	buckets  [rateWindowSeconds]rateBucket
	inFlight int64
}

// bucketFor returns the bucket for the given second, resetting it when the ring
// slot belonged to an older second.
func (w *rateWindow) bucketFor(now time.Time) *rateBucket {
	sec := now.Unix()
	idx := sec % rateWindowSeconds
	if idx < 0 {
		idx += rateWindowSeconds
	}
	b := &w.buckets[idx]
	if b.sec != sec {
		b.sec = sec
		b.reqs = 0
		b.toks = 0
	}
	return b
}

// addRequest records one dispatched request at now.
func (w *rateWindow) addRequest(now time.Time) {
	if w == nil {
		return
	}
	w.bucketFor(now).reqs++
}

// addTokens records token consumption observed at now.
func (w *rateWindow) addTokens(now time.Time, n int64) {
	if w == nil || n <= 0 {
		return
	}
	w.bucketFor(now).toks += n
}

// acquire increments the in-flight counter when a request is dispatched.
func (w *rateWindow) acquire() {
	if w == nil {
		return
	}
	w.inFlight++
}

// release decrements the in-flight counter when a request completes, flooring at zero.
func (w *rateWindow) release() {
	if w == nil || w.inFlight <= 0 {
		return
	}
	w.inFlight--
}

// sums returns the total requests and tokens within the trailing window ending at now.
func (w *rateWindow) sums(now time.Time) (reqs int64, toks int64) {
	if w == nil {
		return 0, 0
	}
	current := now.Unix()
	minSec := current - rateWindowSeconds + 1
	for i := range w.buckets {
		b := w.buckets[i]
		if b.sec >= minSec && b.sec <= current {
			reqs += b.reqs
			toks += b.toks
		}
	}
	return reqs, toks
}

// earliestFree estimates when the trailing window will drop below the limit,
// i.e. the oldest counted second plus the window length. It is used as a
// conservative Retry-After hint. Returns now+1s when nothing is counted.
func (w *rateWindow) earliestFree(now time.Time) time.Time {
	fallback := now.Add(time.Second)
	if w == nil {
		return fallback
	}
	current := now.Unix()
	minSec := current - rateWindowSeconds + 1
	oldest := int64(0)
	found := false
	for i := range w.buckets {
		b := w.buckets[i]
		if (b.reqs > 0 || b.toks > 0) && b.sec >= minSec && b.sec <= current {
			if !found || b.sec < oldest {
				oldest = b.sec
				found = true
			}
		}
	}
	if !found {
		return fallback
	}
	free := time.Unix(oldest+rateWindowSeconds, 0)
	if !free.After(now) {
		return fallback
	}
	return free
}
