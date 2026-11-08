package discovery

import (
	"time"

	"github.com/miekg/dns"
)

// browseQueryRetry keeps discovery backoff independent from cached PTR lifetime.
type browseQueryRetry struct {
	next, last time.Time
	interval   time.Duration
}

func (r *browseQueryRetry) sent(now time.Time) {
	if r.interval == 0 {
		r.interval = time.Second
	} else if r.interval < time.Hour {
		r.interval *= 2
		if r.interval > time.Hour {
			r.interval = time.Hour
		}
	}
	r.last = now
	r.next = now.Add(r.interval)
}

func (c *browseCache) queryDue(index int, q dns.Question, state browseQueryRetry, now time.Time) bool {
	if !now.Before(state.next) {
		return true
	}
	if q.Qtype != dns.TypePTR {
		return false
	}
	// RFC 6762 section 5.2: active browse PTRs must be refreshed even after the
	// discovery backoff has grown longer than their TTL. Retry about every 5%
	// of TTL from 80% onward; never generate sub-second retries.
	for _, r := range c.rrset(index, q.Name, dns.TypePTR) {
		ttl := time.Duration(r.rr.Header().Ttl) * time.Second
		retry := max(time.Second, ttl/20)
		if now.Sub(r.received) >= ttl*4/5 && now.Sub(state.last) >= retry {
			return true
		}
	}
	return false
}

// queryMessages implements known-answer suppression (RFC 6762 sections 7.1 and
// 7.2). Oversized answer lists continue in TC-marked query packets rather than
// forcing IP fragmentation or silently discarding the remaining known answers.
func (c *browseCache) queryMessages(index int, q dns.Question, now time.Time) []*dns.Msg {
	const maxPayload = 1200
	current := &dns.Msg{Question: []dns.Question{q}}
	var messages []*dns.Msg
	for _, r := range c.rrset(index, q.Name, q.Qtype) {
		remaining := r.expires.Sub(now)
		original := time.Duration(r.rr.Header().Ttl) * time.Second
		if remaining <= 0 || remaining < original/2 {
			continue
		}
		rr := dns.Copy(r.rr)
		rr.Header().Class &^= 0x8000
		rr.Header().Ttl = uint32(remaining / time.Second)
		current.Answer = append(current.Answer, rr)
		wire, err := current.Pack()
		if err == nil && len(wire) <= maxPayload {
			continue
		}
		current.Answer = current.Answer[:len(current.Answer)-1]
		current.Truncated = true
		messages = append(messages, current)
		current = &dns.Msg{Answer: []dns.RR{rr}}
	}
	return append(messages, current)
}
