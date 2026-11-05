package ollama

import (
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/MoeForge/moego/ojson"
)

var errDocument = errors.New("invalid Ollama quota document")

func numeric(r ojson.Result) bool {
	return r.Type == ojson.Number && !math.IsNaN(r.Float()) && !math.IsInf(r.Float(), 0)
}

func instant(r ojson.Result) (time.Time, bool) {
	if r.Type != ojson.String {
		return time.Time{}, false
	}
	v, err := time.Parse(time.RFC3339Nano, r.String())
	return v, err == nil
}

func period(r ojson.Result) bool {
	from, okFrom := instant(r.Get("from"))
	until, okUntil := instant(r.Get("until"))
	return r.IsObject() && okFrom && okUntil && until.After(from)
}

func metrics(r ojson.Result) bool {
	if !r.IsObject() {
		return false
	}
	count := r.Get("request_count")
	if !numeric(count) {
		return false
	}
	n, err := strconv.ParseInt(count.Raw, 10, 64)
	if err != nil || n < 0 {
		return false
	}
	for _, name := range []string{"input_tokens", "cached_input_tokens", "output_tokens"} {
		v := r.Get(name)
		if !v.Exists() {
			continue
		}
		if !numeric(v) {
			return false
		}
		n, err := strconv.ParseInt(v.Raw, 10, 64)
		if err != nil || n < 0 {
			return false
		}
	}
	if v := r.Get("usage_usd"); v.Exists() && !numeric(v) {
		return false
	}
	if groups := r.Get("groups"); groups.Exists() {
		if !groups.IsArray() {
			return false
		}
		for _, group := range groups.Array() {
			if group.Get("model").Type != ojson.String || !metrics(group) {
				return false
			}
		}
	}
	if remainder := r.Get("remainder"); remainder.Exists() && !metrics(remainder) {
		return false
	}
	return true
}

func validateDocument(body []byte, target any) error {
	if !ojson.ValidBytes(body) {
		return errDocument
	}
	r := ojson.ParseBytes(body)
	if !r.IsObject() {
		return errDocument
	}
	switch target.(type) {
	case *UsageResponse:
		if !period(r) || !metrics(r.Get("totals")) || !r.Get("buckets").IsArray() {
			return errDocument
		}
		granularity := "day"
		if r.Get("range").String() == "24h" {
			granularity = "hour"
		}
		if r.Get("granularity").String() != granularity {
			return errDocument
		}
		from, _ := instant(r.Get("from"))
		until, _ := instant(r.Get("until"))
		previous := from
		for _, bucket := range r.Get("buckets").Array() {
			if !period(bucket) || !metrics(bucket) {
				return errDocument
			}
			start, _ := instant(bucket.Get("from"))
			end, _ := instant(bucket.Get("until"))
			if start.Before(previous) || end.After(until) {
				return errDocument
			}
			previous = end
			if partial := bucket.Get("partial"); partial.Exists() && partial.Type != ojson.True && partial.Type != ojson.False {
				return errDocument
			}
		}
	case *BalanceResponse:
		included := r.Get("included")
		purchased := r.Get("purchased")
		if !included.IsObject() || !purchased.IsObject() || !numeric(purchased.Get("balance_usd")) {
			return errDocument
		}
		modern := included.Get("balance_usd").Exists() || included.Get("allowance_usd").Exists() || included.Get("period").Exists()
		legacy := included.Get("session").Exists() || included.Get("weekly").Exists()
		if modern == legacy {
			return errDocument
		}
		if modern {
			if !numeric(included.Get("balance_usd")) || !numeric(included.Get("allowance_usd")) || !period(included.Get("period")) {
				return errDocument
			}
		} else {
			for _, name := range []string{"session", "weekly"} {
				limit := included.Get(name)
				percent := limit.Get("remaining_percent")
				_, ok := instant(limit.Get("resets_at"))
				if !limit.IsObject() || !numeric(percent) || percent.Float() < 0 || percent.Float() > 100 || !ok {
					return errDocument
				}
			}
		}
	}
	return nil
}
