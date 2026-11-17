package selfupdatee2e

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
)

const (
	v1    = "8.0.0-proxx.1"
	v2    = "8.0.0-proxx.2"
	v3    = "8.0.0-proxx.3"
	v4    = "8.0.0-proxx.4" // crashes on start
	vDown = "7.9.9-proxx.9"
)

func TestE2ESelfUpdate(t *testing.T) {
	requireE2E(t)
	root := t.TempDir()

	ghLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gh := newMockGitHub(t, ghLn)
	upstream := newMockUpstream(t)

	b := &builder{t: t, dir: filepath.Join(root, "build"), pubB64: gh.pubB64(), apiBase: gh.srv.URL}
	if err = os.MkdirAll(b.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Log("building v1..v4")
	bin1 := b.build(v1, false)
	bin2 := b.build(v2, false)
	bin3 := b.build(v3, false)
	bin4 := b.build(v4, true)
	binDown := b.build(vDown, false)

	in := newInstall(t, root, bin1, upstream.URL)
	sup := startSupervisor(t, in)
	waitVersion(t, in.base, v1, 60*time.Second)

	// Usage counted before the upgrade (scenario 7).
	plain := &http.Client{Timeout: 30 * time.Second}
	for i := 0; i < 5; i++ {
		if res := chat(plain, in.base, "fast", i%2 == 0); res.status != http.StatusOK || !res.done {
			t.Fatalf("warm-up request %d: %+v", i, res)
		}
	}
	var usageBefore int64
	waitFor(t, 15*time.Second, "usage recorded", func() bool {
		_, body := mgmt(t, in.base, http.MethodGet, "/v8/management/proxx/usage/summary", "")
		usageBefore = ojson.Get(body, "totals.requests").Int()
		return usageBefore >= 5
	})

	t.Run("8_unwritable_install_dir_unsupported", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can write anywhere")
		}
		if err := os.Chmod(in.binDir, 0o555); err != nil {
			t.Fatal(err)
		}
		st := status(t, in.base)
		_ = os.Chmod(in.binDir, 0o755)
		if ojson.Get(st, "supported").Bool() || !strings.Contains(ojson.Get(st, "unsupported_reason").String(), "not writable") {
			t.Fatalf("unwritable dir status: %s", st)
		}
		gh.publish(v2, bin2, "good")
		_ = os.Chmod(in.binDir, 0o555)
		code, body := mgmt(t, in.base, http.MethodPost, "/v8/management/proxx/update/apply", "")
		_ = os.Chmod(in.binDir, 0o755)
		if code != http.StatusConflict || ojson.Get(body, "error").String() == "" {
			t.Fatalf("apply while unsupported = %d %s, want 409 {error}", code, body)
		}
		if st = status(t, in.base); !ojson.Get(st, "supported").Bool() {
			t.Fatalf("writable dir status: %s", st)
		}
		t.Logf("scenario 8: supported=false reason=%q; apply -> %d", "not writable", code)
	})

	t.Run("4_refuses_bad_signature_tampered_archive_and_downgrade", func(t *testing.T) {
		for _, mode := range []string{"badsig", "tampered"} {
			gh.publish(v2, bin2, mode)
			code, body := mgmt(t, in.base, http.MethodPost, "/v8/management/proxx/update/check", "")
			if code != http.StatusOK || !ojson.Get(body, "update_available").Bool() || ojson.Get(body, "latest_version").String() != v2 {
				t.Fatalf("%s: check = %d %s", mode, code, body)
			}
			code, body = mgmt(t, in.base, http.MethodPost, "/v8/management/proxx/update/apply", "")
			if code != http.StatusAccepted {
				t.Fatalf("%s: apply = %d %s", mode, code, body)
			}
			var st string
			waitFor(t, 60*time.Second, mode+" refused", func() bool {
				st = status(t, in.base)
				return ojson.Get(st, "state").String() == "failed"
			})
			wantErr := map[string]string{"badsig": "signature", "tampered": "checksum mismatch"}[mode]
			if !strings.Contains(ojson.Get(st, "last_error").String(), wantErr) || ojson.Get(st, "current_version").String() != v1 {
				t.Fatalf("%s: status %s", mode, st)
			}
			if _, errStat := os.Stat(in.exe + ".new"); errStat == nil {
				t.Fatalf("%s: a refused binary was staged", mode)
			}
			if res := chat(plain, in.base, "fast", false); res.status != http.StatusOK || !res.done {
				t.Fatalf("%s: v1 stopped serving: %+v", mode, res)
			}
			t.Logf("scenario 4 %s: refused (%s), v1 still serving", mode, ojson.Get(st, "last_error").String())
		}
		gh.publish(vDown, binDown, "good")
		code, body := mgmt(t, in.base, http.MethodPost, "/v8/management/proxx/update/check", "")
		if code != http.StatusOK || ojson.Get(body, "update_available").Bool() || ojson.Get(body, "latest_version").String() != vDown {
			t.Fatalf("downgrade: check = %d %s", code, body)
		}
		code, body = mgmt(t, in.base, http.MethodPost, "/v8/management/proxx/update/apply", "")
		if code != http.StatusConflict || !strings.Contains(ojson.Get(body, "error").String(), "no newer release") {
			t.Fatalf("downgrade: apply = %d %s, want 409", code, body)
		}
		if res := chat(plain, in.base, "fast", false); res.status != http.StatusOK {
			t.Fatalf("downgrade: v1 stopped serving: %+v", res)
		}
		t.Logf("scenario 4 downgrade: update_available=false, apply -> 409")
	})

	pidBefore, startsBefore := sup.state()

	t.Run("1_2_3_7_upgrade_under_load", func(t *testing.T) {
		gh.publish(v2, bin2, "good")
		if code, body := mgmt(t, in.base, http.MethodPost, "/v8/management/proxx/update/check", ""); code != http.StatusOK || !ojson.Get(body, "update_available").Bool() {
			t.Fatalf("check: %d %s", code, body)
		}
		// ETag: an unchanged release answers 304.
		notModBefore := gh.notMod.Load()
		mgmt(t, in.base, http.MethodPost, "/v8/management/proxx/update/check", "")
		if gh.notMod.Load() != notModBefore+1 {
			t.Errorf("second check did not send If-None-Match (304s: %d -> %d)", notModBefore, gh.notMod.Load())
		}

		lg := startLoad(in.base, 20)
		time.Sleep(2 * time.Second)

		// Long stream started before apply (scenario 2).
		longDone := make(chan chatResult, 1)
		var longEnded time.Time
		go func() {
			client := &http.Client{Timeout: 2 * time.Minute}
			res := chat(client, in.base, "slow", true)
			longEnded = time.Now()
			longDone <- res
		}()
		time.Sleep(500 * time.Millisecond)

		code, body := mgmt(t, in.base, http.MethodPost, "/v8/management/proxx/update/apply", "")
		if code != http.StatusAccepted {
			t.Fatalf("apply = %d %s", code, body)
		}
		var sawWaiting bool
		var maxInflight int64
		waitFor(t, 30*time.Second, "waiting for idle while the long stream runs", func() bool {
			st := status(t, in.base)
			if n := ojson.Get(st, "inflight").Int(); n > maxInflight {
				maxInflight = n
			}
			if ojson.Get(st, "state").String() == "waiting_idle" && ojson.Get(st, "staged_version").String() == v2 {
				sawWaiting = true
			}
			return sawWaiting
		})
		var swapped time.Time
		st := ""
		waitFor(t, 90*time.Second, "v2 running", func() bool {
			st = status(t, in.base)
			if ojson.Get(st, "current_version").String() == v2 {
				swapped = time.Now()
				return true
			}
			return false
		})
		long := <-longDone
		time.Sleep(3 * time.Second) // keep the load running on v2 for a bit
		lg.finish()

		// Scenario 2: the long stream got every chunk and the switch happened after it ended.
		if long.err != nil || long.status != http.StatusOK || long.chunks != slowChunks || !long.done {
			t.Errorf("scenario 2: long stream %+v, want %d chunks + [DONE]", long, slowChunks)
		}
		if !swapped.After(longEnded) {
			t.Errorf("scenario 2: v2 answered at %s, before the long stream ended at %s", swapped, longEnded)
		}
		t.Logf("scenario 2: long stream chunks=%d/%d done=%v; ended %s before v2 answered; max inflight seen %d",
			long.chunks, slowChunks, long.done, swapped.Sub(longEnded).Round(time.Millisecond), maxInflight)

		// Scenario 1: zero failed requests under continuous load.
		t.Logf("scenario 1: raw keep-alive client: requests sent=%d failed=%d retries=0 (never retries) reconnects-after-Connection:close=%d", lg.sent.Load(), lg.failed.Load(), lg.reconnects.Load())
		if lg.failed.Load() != 0 {
			t.Errorf("scenario 1: %d failed requests: %v", lg.failed.Load(), lg.failureSamples())
		}
		if lg.sent.Load() < 100 {
			t.Errorf("scenario 1: only %d requests sent", lg.sent.Load())
		}

		// Scenario 3: same PID, no supervisor restart, v2 reported.
		pidAfter, startsAfter := sup.state()
		if pidAfter != pidBefore || startsAfter != startsBefore || ojson.Get(st, "pid").Int() != int64(pidBefore) {
			t.Errorf("scenario 3: pid %d->%d (status pid %d), supervisor starts %d->%d", pidBefore, pidAfter, ojson.Get(st, "pid").Int(), startsBefore, startsAfter)
		}
		waitFor(t, 30*time.Second, "last_result ok", func() bool {
			st = status(t, in.base)
			return ojson.Get(st, "last_result.ok").Bool()
		})
		if lr := ojson.Get(st, "last_result"); lr.Get("from").String() != v1 || lr.Get("to").String() != v2 || lr.Get("rolled_back").Bool() {
			t.Errorf("scenario 3: last_result %s", lr.Raw)
		}
		if _, errStat := os.Stat(in.exe + ".prev"); errStat != nil {
			t.Errorf("scenario 3: %s.prev missing: %v", in.exe, errStat)
		}
		t.Logf("scenario 3: pid %d unchanged, supervisor starts %d, version %s, last_result %s", pidAfter, startsAfter, ojson.Get(st, "current_version").String(), ojson.Get(st, "last_result").Raw)

		// Scenario 7: usage counted before the upgrade survives it.
		_, body = mgmt(t, in.base, http.MethodGet, "/v8/management/proxx/usage/summary", "")
		usageAfter := ojson.Get(body, "totals.requests").Int()
		if usageAfter < usageBefore+lg.sent.Load()/2 {
			t.Errorf("scenario 7: usage requests before=%d after=%d (sent %d during load)", usageBefore, usageAfter, lg.sent.Load())
		}
		t.Logf("scenario 7: usage totals.requests before=%d after=%d", usageBefore, usageAfter)
	})

	t.Run("6_auto_apply_window", func(t *testing.T) {
		gh.publish(v3, bin3, "good")
		now := time.Now()
		outside := fmt.Sprintf("%02d:00-%02d:00", (now.Hour()+2)%24, (now.Hour()+3)%24)
		putConfig(t, in.base, "window", fmt.Sprintf("%q", outside))
		putConfig(t, in.base, "auto-apply", "true")
		waitFor(t, 15*time.Second, "auto-apply config reloaded", func() bool {
			st := status(t, in.base)
			return ojson.Get(st, "config.auto_apply").Bool() && ojson.Get(st, "config.window").String() == outside
		})
		mgmt(t, in.base, http.MethodPost, "/v8/management/proxx/update/check", "")
		waitFor(t, 60*time.Second, "v3 staged outside the window", func() bool {
			st := status(t, in.base)
			return ojson.Get(st, "state").String() == "staged" && ojson.Get(st, "staged_version").String() == v3
		})
		time.Sleep(8 * time.Second)
		st := status(t, in.base)
		if ojson.Get(st, "current_version").String() != v2 || ojson.Get(st, "state").String() != "staged" {
			t.Fatalf("outside the window an update was applied: %s", st)
		}
		t.Logf("scenario 6: outside window %s -> staged %s, still running %s", outside, v3, v2)
		inside := fmt.Sprintf("%02d:00-%02d:00", now.Add(-time.Hour).Hour(), now.Add(2*time.Hour).Hour())
		putConfig(t, in.base, "window", fmt.Sprintf("%q", inside))
		st = waitVersion(t, in.base, v3, 60*time.Second)
		t.Logf("scenario 6: inside window %s -> applied, now %s", inside, ojson.Get(st, "current_version").String())
		putConfig(t, in.base, "auto-apply", "false")
	})

	t.Run("5_crashing_update_rolls_back", func(t *testing.T) {
		_, startsBeforeCrash := sup.state()
		gh.publish(v4, bin4, "good")
		if code, body := mgmt(t, in.base, http.MethodPost, "/v8/management/proxx/update/check", ""); code != http.StatusOK || !ojson.Get(body, "update_available").Bool() {
			t.Fatalf("check: %d %s", code, body)
		}
		if code, body := mgmt(t, in.base, http.MethodPost, "/v8/management/proxx/update/apply", ""); code != http.StatusAccepted {
			t.Fatalf("apply: %d %s", code, body)
		}
		var st string
		waitFor(t, 120*time.Second, "rollback to v3", func() bool {
			st = status(t, in.base)
			return ojson.Get(st, "current_version").String() == v3 && ojson.Get(st, "last_result.rolled_back").Bool()
		})
		_, startsAfterCrash := sup.state()
		restarts := startsAfterCrash - startsBeforeCrash
		if restarts < 1 || restarts > 2 {
			t.Errorf("supervisor restarts during rollback = %d, want 1..2 (3 start attempts incl. the exec)", restarts)
		}
		lr := ojson.Get(st, "last_result")
		if lr.Get("ok").Bool() || lr.Get("from").String() != v3 || lr.Get("to").String() != v4 || lr.Get("error").String() == "" {
			t.Errorf("last_result after rollback: %s", lr.Raw)
		}
		if res := chat(plain, in.base, "fast", false); res.status != http.StatusOK || !res.done {
			t.Errorf("v3 not serving after rollback: %+v", res)
		}
		t.Logf("scenario 5: rolled back to %s after %d supervisor restarts; last_result %s", v3, restarts, lr.Raw)
	})
}
