//go:build windows || (cgo && (linux || darwin || freebsd))

package pluginhost

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testPluginLibraryExtension() string {
	switch runtime.GOOS {
	case "windows":
		return ".dll"
	case "darwin":
		return ".dylib"
	default:
		return ".so"
	}
}

func TestShadowPluginDirUsesSharedCache(t *testing.T) {
	dir, errDir := shadowPluginDir()
	if errDir != nil {
		t.Fatalf("shadowPluginDir() error = %v", errDir)
	}
	want := filepath.Join(os.TempDir(), "cliproxy-pluginhost", "cache")
	if dir != want {
		t.Fatalf("shadowPluginDir() = %q, want %q", dir, want)
	}
}

func TestShadowCopyPluginReusesAcrossProcessRestartAndUsesLocalShadowDir(t *testing.T) {
	pluginDir := t.TempDir()
	source := filepath.Join(pluginDir, "alpha-v1.0.0"+testPluginLibraryExtension())
	content := []byte("plugin-v1")
	if errWrite := os.WriteFile(source, content, 0o644); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	file := pluginFile{ID: "alpha", Path: source, Version: "1.0.0"}

	first, errFirst := shadowCopyPlugin(file)
	if errFirst != nil {
		t.Fatalf("shadowCopyPlugin() first error = %v", errFirst)
	}
	wantShadowDir := filepath.Join(pluginDir, ".shadow")
	if filepath.Dir(first) != wantShadowDir {
		t.Fatalf("shadowCopyPlugin() dir = %q, want %q", filepath.Dir(first), wantShadowDir)
	}

	past := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	if errCh := os.Chtimes(first, past, past); errCh != nil {
		t.Fatalf("Chtimes(first) error = %v", errCh)
	}
	if errCh := os.Chtimes(wantShadowDir, past, past); errCh != nil {
		t.Fatalf("Chtimes(shadowDir) error = %v", errCh)
	}

	childPath := runChildShadowCopyPlugin(t, file)
	if childPath != first {
		t.Fatalf("child process shadowCopyPlugin() = %q, want reused %q", childPath, first)
	}
	info, errStat := os.Stat(first)
	if errStat != nil {
		t.Fatalf("Stat(%s) after child restart error = %v", first, errStat)
	}
	if !info.ModTime().Equal(past) {
		t.Fatalf("shadow file ModTime = %v, want unchanged %v", info.ModTime(), past)
	}
	dirInfo, errDirStat := os.Stat(wantShadowDir)
	if errDirStat != nil {
		t.Fatalf("Stat(%s) after child restart error = %v", wantShadowDir, errDirStat)
	}
	if !dirInfo.ModTime().Equal(past) {
		t.Fatalf("shadow dir ModTime = %v, want unchanged %v (no temp file should be created on cache hit)", dirInfo.ModTime(), past)
	}
}

func TestShadowCopyPluginToDirSkipsTempWriteWhenShadowMatches(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(t.TempDir(), "alpha"+testPluginLibraryExtension())
	if errWrite := os.WriteFile(source, []byte("plugin-v1"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	file := pluginFile{ID: "alpha", Path: source}

	first, errFirst := shadowCopyPluginToDir(file, dir)
	if errFirst != nil {
		t.Fatalf("shadowCopyPluginToDir() first error = %v", errFirst)
	}
	past := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	if errCh := os.Chtimes(first, past, past); errCh != nil {
		t.Fatalf("Chtimes(first) error = %v", errCh)
	}
	if errCh := os.Chtimes(dir, past, past); errCh != nil {
		t.Fatalf("Chtimes(dir) error = %v", errCh)
	}

	second, errSecond := shadowCopyPluginToDir(file, dir)
	if errSecond != nil {
		t.Fatalf("shadowCopyPluginToDir() second error = %v", errSecond)
	}
	if second != first {
		t.Fatalf("second shadow path = %q, want %q", second, first)
	}
	dirInfo, errDirStat := os.Stat(dir)
	if errDirStat != nil {
		t.Fatalf("Stat(dir) error = %v", errDirStat)
	}
	if !dirInfo.ModTime().Equal(past) {
		t.Fatalf("shadow dir ModTime = %v, want unchanged %v (temp file was written before checking shadowPluginMatches)", dirInfo.ModTime(), past)
	}
}

func TestShadowCopyPluginFallsBackToSharedCacheWhenLocalShadowDirUnavailable(t *testing.T) {
	pluginDir := t.TempDir()
	// Create a regular file named ".shadow" so creating the local .shadow directory fails.
	blockerPath := filepath.Join(pluginDir, shadowPluginLocalDirName)
	if errWrite := os.WriteFile(blockerPath, []byte("blocker"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile(blocker) error = %v", errWrite)
	}
	source := filepath.Join(pluginDir, "fallback-v1.0.0"+testPluginLibraryExtension())
	if errWrite := os.WriteFile(source, []byte("plugin-fallback"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile(source) error = %v", errWrite)
	}
	file := pluginFile{ID: "fallback", Path: source, Version: "1.0.0"}

	got, errShadow := shadowCopyPlugin(file)
	if errShadow != nil {
		t.Fatalf("shadowCopyPlugin() error = %v", errShadow)
	}
	t.Cleanup(func() {
		removeShadowPlugin(got)
	})
	wantDir, errDir := shadowPluginFallbackDir(source)
	if errDir != nil {
		t.Fatalf("shadowPluginFallbackDir() error = %v", errDir)
	}
	if filepath.Dir(got) != wantDir {
		t.Fatalf("shadowCopyPlugin() fallback dir = %q, want %q", filepath.Dir(got), wantDir)
	}
}

func TestShadowCopyPluginFallbackCacheIsolatesDistinctPluginDirs(t *testing.T) {
	ext := testPluginLibraryExtension()
	rootA := t.TempDir()
	platformDirA := filepath.Join(rootA, runtime.GOOS, runtime.GOARCH)
	if errMkdir := os.MkdirAll(platformDirA, 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll(platformDirA) error = %v", errMkdir)
	}
	if errWrite := os.WriteFile(filepath.Join(platformDirA, shadowPluginLocalDirName), []byte("blocker"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile(blockerA) error = %v", errWrite)
	}
	sourceA := filepath.Join(platformDirA, "alpha-v1.0.0"+ext)
	if errWrite := os.WriteFile(sourceA, []byte("alpha-install-A"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile(sourceA) error = %v", errWrite)
	}
	fileA := pluginFile{ID: "alpha", Path: sourceA, Version: "1.0.0"}

	rootB := t.TempDir()
	platformDirB := filepath.Join(rootB, runtime.GOOS, runtime.GOARCH)
	if errMkdir := os.MkdirAll(platformDirB, 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll(platformDirB) error = %v", errMkdir)
	}
	if errWrite := os.WriteFile(filepath.Join(platformDirB, shadowPluginLocalDirName), []byte("blocker"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile(blockerB) error = %v", errWrite)
	}
	sourceB := filepath.Join(platformDirB, "alpha-v1.0.0"+ext)
	if errWrite := os.WriteFile(sourceB, []byte("alpha-install-B"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile(sourceB) error = %v", errWrite)
	}
	fileB := pluginFile{ID: "alpha", Path: sourceB, Version: "1.0.0"}

	shadowA, errShadowA := shadowCopyPlugin(fileA)
	if errShadowA != nil {
		t.Fatalf("shadowCopyPlugin(fileA) error = %v", errShadowA)
	}
	t.Cleanup(func() { removeShadowPluginDir(filepath.Dir(shadowA)) })

	shadowB, errShadowB := shadowCopyPlugin(fileB)
	if errShadowB != nil {
		t.Fatalf("shadowCopyPlugin(fileB) error = %v", errShadowB)
	}
	t.Cleanup(func() { removeShadowPluginDir(filepath.Dir(shadowB)) })

	if filepath.Dir(shadowA) == filepath.Dir(shadowB) {
		t.Fatalf("fallback cache dirs collided across installations: %q", filepath.Dir(shadowA))
	}

	if errCleanup := cleanupUnselectedPluginFiles(rootA, []pluginFile{fileA}); errCleanup != nil {
		t.Fatalf("cleanupUnselectedPluginFiles(rootA) error = %v", errCleanup)
	}
	if _, errStat := os.Stat(shadowB); errStat != nil {
		t.Fatalf("Stat(shadowB %s) after rootA cleanup error = %v, want kept", shadowB, errStat)
	}
}

func TestShadowDirectoryDigestResolvesRelativePathsByWorkingDirectory(t *testing.T) {
	origWD, errWD := os.Getwd()
	if errWD != nil {
		t.Fatalf("Getwd() error = %v", errWD)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origWD)
	})

	workA := t.TempDir()
	workB := t.TempDir()
	relDir := filepath.Join("plugins", runtime.GOOS, runtime.GOARCH)

	if errChdir := os.Chdir(workA); errChdir != nil {
		t.Fatalf("Chdir(workA) error = %v", errChdir)
	}
	digestA := shadowDirectoryDigest(relDir)

	if errChdir := os.Chdir(workB); errChdir != nil {
		t.Fatalf("Chdir(workB) error = %v", errChdir)
	}
	digestB := shadowDirectoryDigest(relDir)

	if digestA == digestB {
		t.Fatalf("shadowDirectoryDigest(%q) collided across working directories: %s", relDir, digestA)
	}
}

func TestShadowCopyPluginConcurrentPublishNeverUnlinksExistingTarget(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(t.TempDir(), "alpha"+testPluginLibraryExtension())
	content := []byte("concurrent-plugin-content")
	if errWrite := os.WriteFile(source, content, 0o644); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	file := pluginFile{ID: "alpha", Path: source}

	first, errFirst := shadowCopyPluginToDir(file, dir)
	if errFirst != nil {
		t.Fatalf("shadowCopyPluginToDir() initial error = %v", errFirst)
	}
	infoBefore, errStatBefore := os.Stat(first)
	if errStatBefore != nil {
		t.Fatalf("Stat(first) error = %v", errStatBefore)
	}
	size, digest, errHash := hashPluginFile(source)
	if errHash != nil {
		t.Fatalf("hashPluginFile() error = %v", errHash)
	}

	// Simulate a racer that already wrote its temp file before winner published target,
	// and now attempts publishShadowTempFile against the already-published target.
	racerTmp := filepath.Join(dir, shadowPluginTempPrefix+"racer"+testPluginLibraryExtension())
	if errWrite := os.WriteFile(racerTmp, content, 0o644); errWrite != nil {
		t.Fatalf("WriteFile(racerTmp) error = %v", errWrite)
	}
	if errPublish := publishShadowTempFile(racerTmp, first, size, digest); errPublish != nil {
		t.Fatalf("publishShadowTempFile() error = %v", errPublish)
	}
	if _, errStatTmp := os.Stat(racerTmp); !os.IsNotExist(errStatTmp) {
		t.Fatalf("Stat(racerTmp) error = %v, want not exist", errStatTmp)
	}
	infoAfter, errStatAfter := os.Stat(first)
	if errStatAfter != nil {
		t.Fatalf("Stat(first) after race error = %v", errStatAfter)
	}
	if !os.SameFile(infoBefore, infoAfter) {
		t.Fatal("existing target shadow file was unlinked/replaced by racer")
	}
}

func TestCleanupUnselectedPluginFilesDoesNotConfusePrefixPluginIDs(t *testing.T) {
	root := t.TempDir()
	platformDir := filepath.Join(root, runtime.GOOS, runtime.GOARCH)
	if errMkdir := os.MkdirAll(platformDir, 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdir)
	}
	ext := testPluginLibraryExtension()
	alphaOldPath := filepath.Join(platformDir, "alpha-v1.0.0"+ext)
	alphaNewPath := filepath.Join(platformDir, "alpha-v2.0.0"+ext)
	alphaBetaPath := filepath.Join(platformDir, "alpha-beta-v1.0.0"+ext)
	for path, content := range map[string]string{
		alphaOldPath:  "alpha-v1",
		alphaNewPath:  "alpha-v2",
		alphaBetaPath: "alpha-beta-v1",
	} {
		if errWrite := os.WriteFile(path, []byte(content), 0o644); errWrite != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, errWrite)
		}
	}
	alphaOldShadow, errOld := shadowCopyPlugin(pluginFile{ID: "alpha", Path: alphaOldPath, Version: "1.0.0"})
	if errOld != nil {
		t.Fatalf("shadowCopyPlugin(alpha old) error = %v", errOld)
	}
	alphaNewFile := pluginFile{ID: "alpha", Path: alphaNewPath, Version: "2.0.0"}
	alphaNewShadow, errNew := shadowCopyPlugin(alphaNewFile)
	if errNew != nil {
		t.Fatalf("shadowCopyPlugin(alpha new) error = %v", errNew)
	}
	alphaBetaShadow, errBeta := shadowCopyPlugin(pluginFile{ID: "alpha-beta", Path: alphaBetaPath, Version: "1.0.0"})
	if errBeta != nil {
		t.Fatalf("shadowCopyPlugin(alpha-beta) error = %v", errBeta)
	}
	removedPath := filepath.Join(platformDir, "removed-v1.0.0"+ext)
	if errWrite := os.WriteFile(removedPath, []byte("removed-v1"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile(removedPath) error = %v", errWrite)
	}
	removedShadow, errRemoved := shadowCopyPlugin(pluginFile{ID: "removed", Path: removedPath, Version: "1.0.0"})
	if errRemoved != nil {
		t.Fatalf("shadowCopyPlugin(removed) error = %v", errRemoved)
	}
	if errRemove := os.Remove(removedPath); errRemove != nil {
		t.Fatalf("Remove(removedPath) error = %v", errRemove)
	}

	if errCleanup := cleanupUnselectedPluginFiles(root, []pluginFile{alphaNewFile}); errCleanup != nil {
		t.Fatalf("cleanupUnselectedPluginFiles() error = %v", errCleanup)
	}
	if _, errStat := os.Stat(alphaOldShadow); !os.IsNotExist(errStat) {
		t.Fatalf("Stat(alphaOldShadow %s) error = %v, want not exist", alphaOldShadow, errStat)
	}
	if _, errStat := os.Stat(removedShadow); !os.IsNotExist(errStat) {
		t.Fatalf("Stat(removedShadow %s) error = %v, want not exist", removedShadow, errStat)
	}
	if _, errStat := os.Stat(alphaNewShadow); errStat != nil {
		t.Fatalf("Stat(alphaNewShadow %s) error = %v, want kept", alphaNewShadow, errStat)
	}
	if _, errStat := os.Stat(alphaBetaShadow); errStat != nil {
		t.Fatalf("Stat(alphaBetaShadow %s) error = %v, want kept for sibling plugin", alphaBetaShadow, errStat)
	}
}

func TestPruneShadowDirPreservesConcurrentlyInstalledPluginAndLiveGuard(t *testing.T) {
	root := t.TempDir()
	platformDir := filepath.Join(root, runtime.GOOS, runtime.GOARCH)
	if errMkdir := os.MkdirAll(platformDir, 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdir)
	}
	ext := testPluginLibraryExtension()
	v1Path := filepath.Join(platformDir, "alpha-v1.0.0"+ext)
	if errWrite := os.WriteFile(v1Path, []byte("alpha-v1"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile(v1Path) error = %v", errWrite)
	}
	v1File := pluginFile{ID: "alpha", Path: v1Path, Version: "1.0.0"}
	v1Shadow, errV1 := shadowCopyPlugin(v1File)
	if errV1 != nil {
		t.Fatalf("shadowCopyPlugin(v1) error = %v", errV1)
	}

	// Simulate another process installing v2 and publishing its shadow file after keepNames was computed for v1.
	v2Path := filepath.Join(platformDir, "alpha-v2.0.0"+ext)
	if errWrite := os.WriteFile(v2Path, []byte("alpha-v2"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile(v2Path) error = %v", errWrite)
	}
	v2File := pluginFile{ID: "alpha", Path: v2Path, Version: "2.0.0"}
	v2Shadow, errV2 := shadowCopyPlugin(v2File)
	if errV2 != nil {
		t.Fatalf("shadowCopyPlugin(v2) error = %v", errV2)
	}

	shadowDir := filepath.Dir(v1Shadow)
	pruneShadowDirForSourceDir(shadowDir, platformDir, []string{filepath.Base(v1Shadow)})
	if _, errStat := os.Stat(v2Shadow); errStat != nil {
		t.Fatalf("Stat(v2Shadow %s) error = %v, want preserved because v2 exists in sourceDir", v2Shadow, errStat)
	}

	// Write v2 content to a separate temp source before removing v2Path from platformDir so we can
	// also test the interleaving where v2 was removed from platformDir while another process holds a guard.
	detachedV2Path := filepath.Join(t.TempDir(), "alpha-v2.0.0"+ext)
	if errWrite := os.WriteFile(detachedV2Path, []byte("alpha-v2"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile(detachedV2Path) error = %v", errWrite)
	}
	v2File = pluginFile{ID: "alpha", Path: detachedV2Path, Version: "2.0.0"}
	if errRemove := os.Remove(v2Path); errRemove != nil {
		t.Fatalf("Remove(v2Path) error = %v", errRemove)
	}
	_, releaseGuard, errAcquire := acquireShadowPluginLoad(v1File)
	if errAcquire != nil {
		t.Fatalf("acquireShadowPluginLoad() error = %v", errAcquire)
	}
	pruneShadowDirForSourceDir(shadowDir, platformDir, []string{filepath.Base(v1Shadow)})
	if _, errStat := os.Stat(v2Shadow); errStat != nil {
		releaseGuard()
		t.Fatalf("Stat(v2Shadow %s) while load guard active error = %v, want preserved", v2Shadow, errStat)
	}
	releaseGuard()

	pruneShadowDirForSourceDir(shadowDir, platformDir, []string{filepath.Base(v1Shadow)})
	if _, errStat := os.Stat(v2Shadow); !os.IsNotExist(errStat) {
		t.Fatalf("Stat(v2Shadow %s) after guard released error = %v, want not exist", v2Shadow, errStat)
	}

	// Verify deterministic interleaving: pruneShadowDirForSourceDir has already read directory entries,
	// and before it acquires the coord lock to delete a candidate shadow file, another caller acquires a load guard.
	v2ShadowRe, errV2Re := shadowCopyPluginToDir(v2File, shadowDir)
	if errV2Re != nil {
		t.Fatalf("shadowCopyPluginToDir(v2) error = %v", errV2Re)
	}
	var releaseInterleavedGuard func()
	origHook := pruneShadowBeforeLockHook
	t.Cleanup(func() {
		pruneShadowBeforeLockHook = origHook
		if releaseInterleavedGuard != nil {
			releaseInterleavedGuard()
		}
	})
	pruneShadowBeforeLockHook = func(shadowFileName string) {
		if shadowFileName == filepath.Base(v2ShadowRe) && releaseInterleavedGuard == nil {
			_, release, errLoad := acquireShadowPluginLoad(v1File)
			if errLoad != nil {
				t.Errorf("interleaved acquireShadowPluginLoad() error = %v", errLoad)
				return
			}
			releaseInterleavedGuard = release
		}
	}
	pruneShadowDirForSourceDir(shadowDir, platformDir, []string{filepath.Base(v1Shadow)})
	pruneShadowBeforeLockHook = origHook
	if _, errStat := os.Stat(v2ShadowRe); errStat != nil {
		t.Fatalf("Stat(v2ShadowRe %s) after interleaved guard acquisition error = %v, want preserved", v2ShadowRe, errStat)
	}
	// Even when keepNames is empty (e.g., all source files were removed or failed hashing),
	// an active load guard must still prevent pruning shadow files.
	pruneShadowDirForSourceDir(shadowDir, platformDir, nil)
	if _, errStat := os.Stat(v2ShadowRe); errStat != nil {
		t.Fatalf("Stat(v2ShadowRe %s) with empty keepNames while guard active error = %v, want preserved", v2ShadowRe, errStat)
	}
	if releaseInterleavedGuard != nil {
		releaseInterleavedGuard()
		releaseInterleavedGuard = nil
	}

	// Verify that if a process crashes/exits abnormally while holding the OS coordination lock,
	// the kernel automatically releases the lock so subsequent loads and pruning recover immediately.
	cmd := exec.Command(os.Args[0], "-test.run=^TestShadowHelperHoldLockAndExit$")
	cmd.Env = append(
		os.Environ(),
		"GO_WANT_SHADOW_HOLD_LOCK_EXIT_HELPER=1",
		"CLIPROXY_SHADOW_HELPER_SOURCE_DIR="+platformDir,
		"CLIPROXY_SHADOW_HELPER_SHADOW_DIR="+shadowDir,
	)
	if output, errRun := cmd.CombinedOutput(); errRun == nil {
		t.Fatalf("helper unexpectedly exited with status 0: %s", string(output))
	}
	_, releaseAfterCrash, errAfterCrash := acquireShadowPluginLoad(v1File)
	if errAfterCrash != nil {
		t.Fatalf("acquireShadowPluginLoad() after crashing lock holder error = %v", errAfterCrash)
	}
	releaseAfterCrash()
	pruneShadowDirForSourceDir(shadowDir, platformDir, []string{filepath.Base(v1Shadow)})
	if _, errStat := os.Stat(v2ShadowRe); !os.IsNotExist(errStat) {
		t.Fatalf("Stat(v2ShadowRe %s) after crashing lock holder and prune error = %v, want not exist", v2ShadowRe, errStat)
	}
}

func TestRemoveStaleShadowTempFilesPreservesLiveProcessTempFile(t *testing.T) {
	dir := t.TempDir()
	ext := testPluginLibraryExtension()
	currentPID := os.Getpid()
	deadPID := startExitedProcessPID(t)
	if deadPID == currentPID {
		t.Skip("exited helper process reused the test pid")
	}
	liveOther, liveOtherPID := startWaitingProcess(t)
	if liveOtherPID == currentPID || liveOtherPID == deadPID {
		t.Skip("live helper process pid collided with another test pid")
	}
	defer stopWaitingProcess(t, liveOther)

	selfTemp := filepath.Join(dir, fmt.Sprintf("%s%s%d-alpha-self%s", shadowPluginTempPrefix, shadowPluginProcessDirPrefix, currentPID, ext))
	liveTemp := filepath.Join(dir, fmt.Sprintf("%s%s%d-alpha-live%s", shadowPluginTempPrefix, shadowPluginProcessDirPrefix, liveOtherPID, ext))
	deadTemp := filepath.Join(dir, fmt.Sprintf("%s%s%d-alpha-dead%s", shadowPluginTempPrefix, shadowPluginProcessDirPrefix, deadPID, ext))
	legacyTemp := filepath.Join(dir, shadowPluginTempPrefix+"alpha-legacy"+ext)
	for _, path := range []string{selfTemp, liveTemp, deadTemp, legacyTemp} {
		if errWrite := os.WriteFile(path, []byte("temp"), 0o644); errWrite != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, errWrite)
		}
	}

	removeStaleShadowTempFiles(dir)

	for _, deadPath := range []string{deadTemp, legacyTemp} {
		if _, errStat := os.Stat(deadPath); !os.IsNotExist(errStat) {
			t.Fatalf("Stat(%s) error = %v, want not exist", deadPath, errStat)
		}
	}
	for _, livePath := range []string{selfTemp, liveTemp} {
		if _, errStat := os.Stat(livePath); errStat != nil {
			t.Fatalf("Stat(%s) error = %v, want kept for live process", livePath, errStat)
		}
	}
}

func TestCleanupUnselectedPluginFilesPrunesStaleShadowPlugins(t *testing.T) {
	root := t.TempDir()
	platformDir := filepath.Join(root, runtime.GOOS, runtime.GOARCH)
	if errMkdir := os.MkdirAll(platformDir, 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdir)
	}
	ext := testPluginLibraryExtension()
	oldPath := filepath.Join(platformDir, "alpha-v1.0.0"+ext)
	newPath := filepath.Join(platformDir, "alpha-v2.0.0"+ext)
	if errWrite := os.WriteFile(oldPath, []byte("alpha-v1"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile(oldPath) error = %v", errWrite)
	}
	if errWrite := os.WriteFile(newPath, []byte("alpha-v2"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile(newPath) error = %v", errWrite)
	}
	oldShadow, errOldShadow := shadowCopyPlugin(pluginFile{ID: "alpha", Path: oldPath, Version: "1.0.0"})
	if errOldShadow != nil {
		t.Fatalf("shadowCopyPlugin(old) error = %v", errOldShadow)
	}
	newFile := pluginFile{ID: "alpha", Path: newPath, Version: "2.0.0"}
	newShadow, errNewShadow := shadowCopyPlugin(newFile)
	if errNewShadow != nil {
		t.Fatalf("shadowCopyPlugin(new) error = %v", errNewShadow)
	}
	tempShadow := filepath.Join(filepath.Dir(newShadow), shadowPluginTempPrefix+"alpha-stale"+ext)
	if errWrite := os.WriteFile(tempShadow, []byte("partial"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile(tempShadow) error = %v", errWrite)
	}

	if errCleanup := cleanupUnselectedPluginFiles(root, []pluginFile{newFile}); errCleanup != nil {
		t.Fatalf("cleanupUnselectedPluginFiles() error = %v", errCleanup)
	}

	if _, errStat := os.Stat(oldShadow); !os.IsNotExist(errStat) {
		t.Fatalf("Stat(oldShadow %s) error = %v, want not exist", oldShadow, errStat)
	}
	if _, errStat := os.Stat(tempShadow); !os.IsNotExist(errStat) {
		t.Fatalf("Stat(tempShadow %s) error = %v, want not exist", tempShadow, errStat)
	}
	if _, errStat := os.Stat(newShadow); errStat != nil {
		t.Fatalf("Stat(newShadow %s) error = %v, want kept", newShadow, errStat)
	}
}

func TestShadowCopyPluginReusesContentAddressedShadow(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(t.TempDir(), "alpha"+testPluginLibraryExtension())
	content := []byte("plugin-v1")
	if errWrite := os.WriteFile(source, content, 0o644); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	file := pluginFile{ID: "alpha", Path: source}

	first, errFirst := shadowCopyPluginToDir(file, dir)
	if errFirst != nil {
		t.Fatalf("shadowCopyPluginToDir() first error = %v", errFirst)
	}
	second, errSecond := shadowCopyPluginToDir(file, dir)
	if errSecond != nil {
		t.Fatalf("shadowCopyPluginToDir() second error = %v", errSecond)
	}

	if second != first {
		t.Fatalf("second shadow path = %q, want reused path %q", second, first)
	}
	gotContent, errRead := os.ReadFile(first)
	if errRead != nil {
		t.Fatalf("ReadFile(%s) error = %v", first, errRead)
	}
	if string(gotContent) != string(content) {
		t.Fatalf("shadow content = %q, want %q", gotContent, content)
	}
	digest := sha256.Sum256(content)
	wantDigest := hex.EncodeToString(digest[:])[:shadowPluginDigestLength]
	name := filepath.Base(first)
	if !strings.HasPrefix(name, shadowPluginPrefix+"alpha-") || !strings.Contains(name, wantDigest) {
		t.Fatalf("shadow file name = %q, want alpha content digest %s", name, wantDigest)
	}
	if count := countShadowPluginFiles(t, dir); count != 1 {
		t.Fatalf("shadow file count = %d, want 1", count)
	}
}

func TestShadowCopyPluginCreatesNewPathForChangedContent(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(t.TempDir(), "alpha"+testPluginLibraryExtension())
	file := pluginFile{ID: "alpha", Path: source}
	if errWrite := os.WriteFile(source, []byte("plugin-v1"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile() v1 error = %v", errWrite)
	}
	first, errFirst := shadowCopyPluginToDir(file, dir)
	if errFirst != nil {
		t.Fatalf("shadowCopyPluginToDir() v1 error = %v", errFirst)
	}

	if errWrite := os.WriteFile(source, []byte("plugin-v2"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile() v2 error = %v", errWrite)
	}
	second, errSecond := shadowCopyPluginToDir(file, dir)
	if errSecond != nil {
		t.Fatalf("shadowCopyPluginToDir() v2 error = %v", errSecond)
	}

	if second == first {
		t.Fatalf("second shadow path reused %q after content changed", second)
	}
	if count := countShadowPluginFiles(t, dir); count != 2 {
		t.Fatalf("shadow file count = %d, want 2 versions", count)
	}
}

func TestShadowCopyPluginReplacesCorruptSameSizeShadow(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(t.TempDir(), "alpha"+testPluginLibraryExtension())
	content := []byte("plugin-v1")
	if errWrite := os.WriteFile(source, content, 0o644); errWrite != nil {
		t.Fatalf("WriteFile() source error = %v", errWrite)
	}
	digest := sha256.Sum256(content)
	target := shadowPluginPath(dir, "alpha", hex.EncodeToString(digest[:]), testPluginLibraryExtension())
	if errWrite := os.WriteFile(target, []byte("corrupt!!"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile() corrupt shadow error = %v", errWrite)
	}

	gotPath, errCopy := shadowCopyPluginToDir(pluginFile{ID: "alpha", Path: source}, dir)
	if errCopy != nil {
		t.Fatalf("shadowCopyPluginToDir() error = %v", errCopy)
	}

	if gotPath != target {
		t.Fatalf("shadow path = %q, want %q", gotPath, target)
	}
	gotContent, errRead := os.ReadFile(target)
	if errRead != nil {
		t.Fatalf("ReadFile(%s) error = %v", target, errRead)
	}
	if string(gotContent) != string(content) {
		t.Fatalf("shadow content = %q, want %q", gotContent, content)
	}
	if count := countShadowPluginFiles(t, dir); count != 1 {
		t.Fatalf("shadow file count = %d, want 1", count)
	}
}

func TestRemoveStaleShadowPluginsOnlyRemovesShadowFiles(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, shadowPluginPrefix+"alpha-deadbeef"+testPluginLibraryExtension())
	temp := filepath.Join(dir, shadowPluginTempPrefix+"alpha-temp"+testPluginLibraryExtension())
	keep := filepath.Join(dir, "keep"+testPluginLibraryExtension())
	for _, path := range []string{stale, temp, keep} {
		if errWrite := os.WriteFile(path, []byte("x"), 0o644); errWrite != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, errWrite)
		}
	}

	removeStaleShadowPlugins(dir)

	for _, path := range []string{stale, temp} {
		if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
			t.Fatalf("Stat(%s) error = %v, want not exist", path, errStat)
		}
	}
	if _, errStat := os.Stat(keep); errStat != nil {
		t.Fatalf("Stat(%s) error = %v, want kept", keep, errStat)
	}
}

func TestRemoveStaleShadowPluginDirsOnlyRemovesDeadProcesses(t *testing.T) {
	base := t.TempDir()
	currentPID := os.Getpid()
	deadPID := startExitedProcessPID(t)
	if deadPID == currentPID {
		t.Skip("exited helper process reused the test pid")
	}
	liveDir := filepath.Join(base, shadowPluginProcessDirName(currentPID))
	deadDir := filepath.Join(base, shadowPluginProcessDirName(deadPID))
	otherDir := filepath.Join(base, "unrelated")
	// A directory owned by a different live process must survive the
	// liveness check itself, not just the current-pid shortcut.
	liveOther, liveOtherPID := startWaitingProcess(t)
	if liveOtherPID == currentPID || liveOtherPID == deadPID {
		t.Skip("live helper process pid collided with another test pid")
	}
	liveOtherDir := filepath.Join(base, shadowPluginProcessDirName(liveOtherPID))
	for _, dir := range []string{liveDir, deadDir, otherDir, liveOtherDir} {
		if errMkdir := os.MkdirAll(dir, 0o755); errMkdir != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, errMkdir)
		}
		shadowFile := filepath.Join(dir, shadowPluginPrefix+"alpha-deadbeef"+testPluginLibraryExtension())
		if errWrite := os.WriteFile(shadowFile, []byte("x"), 0o644); errWrite != nil {
			t.Fatalf("WriteFile(%s) error = %v", shadowFile, errWrite)
		}
	}

	removeStaleShadowPluginDirs(base, currentPID)

	if _, errStat := os.Stat(deadDir); !os.IsNotExist(errStat) {
		t.Fatalf("Stat(%s) error = %v, want not exist", deadDir, errStat)
	}
	for _, dir := range []string{liveDir, otherDir, liveOtherDir} {
		if _, errStat := os.Stat(dir); errStat != nil {
			t.Fatalf("Stat(%s) error = %v, want kept", dir, errStat)
		}
	}
	stopWaitingProcess(t, liveOther)
}

func startExitedProcessPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestShadowHelperProcessExits$")
	cmd.Env = append(os.Environ(), "CLIPROXY_SHADOW_HELPER=1")
	if errRun := cmd.Run(); errRun != nil {
		t.Fatalf("helper process error = %v", errRun)
	}
	if cmd.Process == nil {
		t.Fatal("helper process is nil")
	}
	return cmd.Process.Pid
}

func TestShadowHelperProcessExits(t *testing.T) {
	if os.Getenv("CLIPROXY_SHADOW_HELPER") != "1" {
		t.Skip("helper process entry point")
	}
}

func runChildShadowCopyPlugin(t *testing.T, file pluginFile) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestShadowHelperCopyPlugin$")
	cmd.Env = append(
		os.Environ(),
		"CLIPROXY_SHADOW_COPY_HELPER=1",
		"CLIPROXY_SHADOW_COPY_ID="+file.ID,
		"CLIPROXY_SHADOW_COPY_PATH="+file.Path,
		"CLIPROXY_SHADOW_COPY_VERSION="+file.Version,
	)
	out, errRun := cmd.CombinedOutput()
	if errRun != nil {
		t.Fatalf("child shadowCopyPlugin helper error = %v\n%s", errRun, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "SHADOW_PATH=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "SHADOW_PATH="))
		}
	}
	t.Fatalf("child shadowCopyPlugin output missing SHADOW_PATH: %s", out)
	return ""
}

func TestShadowHelperCopyPlugin(t *testing.T) {
	if os.Getenv("CLIPROXY_SHADOW_COPY_HELPER") != "1" {
		t.Skip("shadow copy helper process entry point")
	}
	got, errShadow := shadowCopyPlugin(pluginFile{
		ID:      os.Getenv("CLIPROXY_SHADOW_COPY_ID"),
		Path:    os.Getenv("CLIPROXY_SHADOW_COPY_PATH"),
		Version: os.Getenv("CLIPROXY_SHADOW_COPY_VERSION"),
	})
	if errShadow != nil {
		t.Fatalf("shadowCopyPlugin() error = %v", errShadow)
	}
	fmt.Printf("SHADOW_PATH=%s\n", got)
}

func startWaitingProcess(t *testing.T) (*exec.Cmd, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestShadowHelperProcessWaits$")
	cmd.Env = append(os.Environ(), "CLIPROXY_SHADOW_HELPER_WAIT=1")
	stdin, errStdin := cmd.StdinPipe()
	if errStdin != nil {
		t.Fatalf("StdinPipe() error = %v", errStdin)
	}
	if errStart := cmd.Start(); errStart != nil {
		t.Fatalf("start waiting helper error = %v", errStart)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	})
	if cmd.Process == nil {
		t.Fatal("waiting helper process is nil")
	}
	return cmd, cmd.Process.Pid
}

func stopWaitingProcess(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

func TestShadowHelperHoldLockAndExit(t *testing.T) {
	if os.Getenv("GO_WANT_SHADOW_HOLD_LOCK_EXIT_HELPER") != "1" {
		t.Skip("crash lock helper entry point")
	}
	sourceDir := os.Getenv("CLIPROXY_SHADOW_HELPER_SOURCE_DIR")
	shadowDir := os.Getenv("CLIPROXY_SHADOW_HELPER_SHADOW_DIR")
	if sourceDir == "" || shadowDir == "" {
		os.Exit(2)
	}
	// Acquire an exclusive coordination lock and exit abnormally without unlocking.
	if _, errLock := acquireShadowCoordLock(sourceDir, shadowDir, true, false); errLock != nil {
		os.Exit(3)
	}
	os.Exit(1)
}

func TestShadowHelperProcessWaits(t *testing.T) {
	if os.Getenv("CLIPROXY_SHADOW_HELPER_WAIT") != "1" {
		t.Skip("waiting helper process entry point")
	}
	buf := make([]byte, 1)
	_, _ = os.Stdin.Read(buf)
}

func countShadowPluginFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		t.Fatalf("ReadDir(%s) error = %v", dir, errRead)
	}
	count := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), shadowPluginPrefix) {
			count++
		}
		if strings.HasPrefix(entry.Name(), shadowPluginTempPrefix) {
			t.Fatalf("temporary shadow file was not cleaned up: %s", entry.Name())
		}
	}
	return count
}
