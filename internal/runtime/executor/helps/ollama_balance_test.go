package helps

import (
	"testing"
	"time"
)

func TestParseOllamaSettingsHTML(t *testing.T) {
	html := []byte(`
<div class="flex flex-col space-y-6">
  <h2 class="text-xl font-medium flex items-center space-x-2">
    <span>Cloud Usage</span>
    <span class="text-xs font-normal px-2 py-0.5 rounded-full bg-neutral-100 text-neutral-600 capitalize">pro</span>
  </h2>
  <div>
    <div class="flex justify-between mb-2">
      <span class="text-sm">Session usage</span>
      <span class="text-sm">21.2% used</span>
    </div>
    <div class="w-full border border-1 border-neutral-200 rounded-full h-2 overflow-hidden">
      <div class="h-full rounded-full bg-neutral-300" style="width: 21.2%"></div>
    </div>
    <div class="text-xs text-neutral-500 mt-1 local-time" data-time="2026-05-05T13:00:00Z">
      Resets in 4 hours
    </div>
  </div>
  <div>
    <div class="flex justify-between mb-2">
      <span class="text-sm">Weekly usage</span>
      <span class="text-sm">35.3% used</span>
    </div>
    <div class="w-full border border-1 border-neutral-200 rounded-full h-2 overflow-hidden">
      <div class="h-full rounded-full bg-neutral-300" style="width: 35.3%"></div>
    </div>
    <div class="text-xs text-neutral-500 mt-1 local-time" data-time="2026-05-11T00:00:00Z">
      Resets in 5 days
    </div>
  </div>
</div>
`)

	balance, err := parseOllamaSettingsHTML(html)
	if err != nil {
		t.Fatalf("parseOllamaSettingsHTML returned error: %v", err)
	}

	if balance.Plan != "pro" {
		t.Errorf("Plan = %q, want %q", balance.Plan, "pro")
	}
	if balance.SessionUsagePct != 21.2 {
		t.Errorf("SessionUsagePct = %v, want 21.2", balance.SessionUsagePct)
	}
	if balance.WeeklyUsagePct != 35.3 {
		t.Errorf("WeeklyUsagePct = %v, want 35.3", balance.WeeklyUsagePct)
	}

	sessionExpected, _ := time.Parse(time.RFC3339, "2026-05-05T13:00:00Z")
	if !balance.SessionResetsAt.Equal(sessionExpected) {
		t.Errorf("SessionResetsAt = %v, want %v", balance.SessionResetsAt, sessionExpected)
	}

	weeklyExpected, _ := time.Parse(time.RFC3339, "2026-05-11T00:00:00Z")
	if !balance.WeeklyResetsAt.Equal(weeklyExpected) {
		t.Errorf("WeeklyResetsAt = %v, want %v", balance.WeeklyResetsAt, weeklyExpected)
	}
}

func TestParseOllamaSettingsHTMLFreePlan(t *testing.T) {
	html := []byte(`
<div class="flex flex-col space-y-6">
  <h2 class="text-xl font-medium flex items-center space-x-2">
    <span>Cloud Usage</span>
    <span class="text-xs font-normal px-2 py-0.5 rounded-full bg-neutral-100 text-neutral-600 capitalize">free</span>
  </h2>
  <div>
    <div class="flex justify-between mb-2">
      <span class="text-sm">Session usage</span>
      <span class="text-sm">0% used</span>
    </div>
    <div class="text-xs text-neutral-500 mt-1 local-time" data-time="2026-05-05T18:00:00Z">
      Resets in 5 hours
    </div>
  </div>
  <div>
    <div class="flex justify-between mb-2">
      <span class="text-sm">Weekly usage</span>
      <span class="text-sm">5% used</span>
    </div>
    <div class="text-xs text-neutral-500 mt-1 local-time" data-time="2026-05-12T00:00:00Z">
      Resets in 7 days
    </div>
  </div>
</div>
`)

	balance, err := parseOllamaSettingsHTML(html)
	if err != nil {
		t.Fatalf("parseOllamaSettingsHTML returned error: %v", err)
	}

	if balance.Plan != "free" {
		t.Errorf("Plan = %q, want %q", balance.Plan, "free")
	}
	if balance.SessionUsagePct != 0 {
		t.Errorf("SessionUsagePct = %v, want 0", balance.SessionUsagePct)
	}
	if balance.WeeklyUsagePct != 5 {
		t.Errorf("WeeklyUsagePct = %v, want 5", balance.WeeklyUsagePct)
	}
}

func TestParseOllamaSettingsHTMLNoData(t *testing.T) {
	html := []byte(`<html><body>Not logged in</body></html>`)
	_, err := parseOllamaSettingsHTML(html)
	if err == nil {
		t.Fatal("expected error for HTML without usage data")
	}
}
