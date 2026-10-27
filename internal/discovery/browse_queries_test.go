package discovery

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestBrowseQueriesRefreshPTRBeforeExpiry(t *testing.T) {
	c := testBrowseCache("")
	records := browseTestRecords()
	for i := range records {
		records[i] = strings.Replace(records[i], "120 IN", "3600 IN", 1)
	}
	records[0] = strings.Replace(records[0], "3600 IN", "10 IN", 1)
	browseTestIngest(t, c, 7, browseTestNow, records...)
	var state browseQueryRetry
	q := dns.Question{Name: c.browse, Qtype: dns.TypePTR, Qclass: dns.ClassINET}
	for second := 0; second <= 120; second++ {
		now := browseTestNow.Add(time.Duration(second) * time.Second)
		c.prune(now)
		if c.queryDue(7, q, state, now) {
			state.sent(now)
			messages := c.queryMessages(7, q, now)
			// A normal peer suppresses a response while our known answer is fresh.
			if len(messages[0].Answer) == 0 {
				browseTestIngest(t, c, 7, now, records[0])
			}
		}
		if len(c.results(now)) != 1 {
			t.Fatalf("online service lost at second %d; next query %v", second, state.next)
		}
	}
}

func TestBrowseQueriesKnownAnswersAndContinuation(t *testing.T) {
	c := testBrowseCache("")
	for i := 0; i < 256; i++ {
		browseTestIngest(t, c, 7, browseTestNow, fmt.Sprintf("_ai-gateway._tcp.local. 120 IN PTR %s%d._ai-gateway._tcp.local.", strings.Repeat("x", 45), i))
	}
	q := dns.Question{Name: c.browse, Qtype: dns.TypePTR, Qclass: dns.ClassINET}
	messages := c.queryMessages(7, q, browseTestNow.Add(20*time.Second))
	count := 0
	if len(messages) < 2 {
		t.Fatal("expected continuation packets")
	}
	for i, m := range messages {
		wire, err := m.Pack()
		if err != nil || len(wire) > 1200 {
			t.Fatalf("packet %d size=%d err=%v", i, len(wire), err)
		}
		if m.Truncated != (i < len(messages)-1) {
			t.Fatalf("packet %d invalid TC", i)
		}
		if (len(m.Question) == 1) != (i == 0) {
			t.Fatalf("packet %d invalid questions", i)
		}
		for _, rr := range m.Answer {
			if rr.Header().Ttl != 100 || rr.Header().Class != dns.ClassINET {
				t.Fatalf("bad known answer: %v", rr)
			}
		}
		count += len(m.Answer)
	}
	if count != 256 {
		t.Fatalf("known answer count=%d", count)
	}
	if late := c.queryMessages(7, q, browseTestNow.Add(61*time.Second)); len(late) != 1 || len(late[0].Answer) != 0 {
		t.Fatal("stale known answers would suppress refresh")
	}
}
