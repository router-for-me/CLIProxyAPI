package selfupdatee2e

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
)

// TestE2ELaunchd upgrades a proxx that runs as a real (temporary) launchd agent, under load, and checks
// that launchd sees the same PID throughout. It never touches the user's own proxx service
// (io.github.iamkunal9.proxx on port 8317, ~/.proxx): everything lives in a temp dir with a unique label
// and a free port, and the agent is booted out at the end.
//
//	PROXX_E2E=1 PROXX_E2E_LAUNCHD=1 go test ./test/proxx/selfupdate_e2e/ -run E2ELaunchd -v -count=1
func TestE2ELaunchd(t *testing.T) {
	requireE2E(t)
	if runtime.GOOS != "darwin" || os.Getenv("PROXX_E2E_LAUNCHD") != "1" {
		t.Skip("set PROXX_E2E_LAUNCHD=1 on macOS to run the launchd e2e")
	}
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
	bin1 := b.build(v1, false)
	bin2 := b.build(v2, false)
	in := newInstall(t, root, bin1, upstream.URL)

	label := fmt.Sprintf("io.github.iamkunal9.proxx.e2e.%d.%d", os.Getpid(), time.Now().Unix())
	if label == "io.github.iamkunal9.proxx" || in.port == 8317 {
		t.Fatal("refusing to use the user's proxx label or port")
	}
	logPath := filepath.Join(root, "launchd.log")
	plist := filepath.Join(root, label+".plist")
	// Same shape as install.sh writes.
	plistBody := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array><string>%s</string><string>--config</string><string>%s</string><string>--local-model</string></array>
	<key>WorkingDirectory</key><string>%s</string>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>ExitTimeOut</key><integer>0</integer>
	<key>StandardOutPath</key><string>%s</string>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, label, in.exe, in.cfgPath, in.home, logPath, logPath)
	if err = os.WriteFile(plist, []byte(plistBody), 0o644); err != nil {
		t.Fatal(err)
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	if out, errBoot := exec.Command("launchctl", "bootstrap", domain, plist).CombinedOutput(); errBoot != nil {
		t.Fatalf("launchctl bootstrap: %v\n%s", errBoot, out)
	}
	t.Cleanup(func() {
		out, errOut := exec.Command("launchctl", "bootout", domain+"/"+label).CombinedOutput()
		t.Logf("launchctl bootout %s: %v %s", label, errOut, out)
		if t.Failed() {
			data, _ := os.ReadFile(logPath)
			if len(data) > 12000 {
				data = data[len(data)-12000:]
			}
			t.Logf("launchd agent log tail:\n%s", data)
		}
	})

	pidRe := regexp.MustCompile(`(?m)^\s*pid = (\d+)`)
	launchdPID := func() int {
		out, errPrint := exec.Command("launchctl", "print", domain+"/"+label).CombinedOutput()
		if errPrint != nil {
			return 0
		}
		m := pidRe.FindSubmatch(out)
		if m == nil {
			return 0
		}
		n, _ := strconv.Atoi(string(m[1]))
		return n
	}

	waitVersion(t, in.base, v1, 90*time.Second)
	pidBefore := launchdPID()
	if pidBefore == 0 {
		t.Fatal("launchd reports no pid")
	}
	gh.publish(v2, bin2, "good")
	if code, body := mgmt(t, in.base, http.MethodPost, "/v8/management/proxx/update/check", ""); code != http.StatusOK || !ojson.Get(body, "update_available").Bool() {
		t.Fatalf("check: %d %s", code, body)
	}
	lg := startLoad(in.base, 20)
	time.Sleep(2 * time.Second)
	if code, body := mgmt(t, in.base, http.MethodPost, "/v8/management/proxx/update/apply", ""); code != http.StatusAccepted {
		t.Fatalf("apply: %d %s", code, body)
	}
	st := waitVersion(t, in.base, v2, 90*time.Second)
	time.Sleep(3 * time.Second)
	lg.finish()
	pidAfter := launchdPID()

	t.Logf("launchd: label=%s port=%d pid %d -> %d (status pid %d); requests sent=%d failed=%d connection-retries=%d",
		label, in.port, pidBefore, pidAfter, ojson.Get(st, "pid").Int(), lg.sent.Load(), lg.failed.Load(), lg.retries.Load())
	if pidAfter != pidBefore || ojson.Get(st, "pid").Int() != int64(pidBefore) {
		t.Errorf("launchd pid changed: %d -> %d", pidBefore, pidAfter)
	}
	if lg.failed.Load() != 0 {
		t.Errorf("%d failed requests: %v", lg.failed.Load(), lg.failureSamples())
	}
	waitFor(t, 30*time.Second, "last_result ok", func() bool {
		return ojson.Get(status(t, in.base), "last_result.ok").Bool()
	})
}
