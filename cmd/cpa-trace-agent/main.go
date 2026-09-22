package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type config struct {
	Bucket          string
	Region          string
	Endpoint        string
	UseSSL          bool
	LogDir          string
	StageDir        string
	RawPrefix       string
	MetaPrefix      string
	BillingPrefix   string
	Settle          time.Duration
	UploadInterval  time.Duration
	DiskWarnMB      int64
	MaxRawFiles     int
	BaseURL         string
	ManagementKey   string
	UsageSpoolDir   string
	BillingSpoolDir string
	PollInterval    time.Duration
	PollBatch       int
}

type agent struct {
	cfg         config
	s3          *minio.Client
	host        string
	client      *http.Client
	billingSeen map[string]struct{}
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fatal(err)
	}
	client, err := newS3Client(cfg)
	if err != nil {
		fatal(err)
	}
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "unknown-host"
	}
	host = strings.Split(host, ".")[0]

	a := &agent{cfg: cfg, s3: client, host: host, client: &http.Client{Timeout: 15 * time.Second}, billingSeen: make(map[string]struct{})}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(cfg.StageDir, 0o755); err != nil {
		fatal(fmt.Errorf("create stage dir: %w", err))
	}
	if cfg.ManagementKey != "" {
		if err := os.MkdirAll(cfg.UsageSpoolDir, 0o755); err != nil {
			fatal(fmt.Errorf("create usage spool dir: %w", err))
		}
		if err := os.MkdirAll(cfg.BillingSpoolDir, 0o755); err != nil {
			fatal(fmt.Errorf("create billing spool dir: %w", err))
		}
		a.loadBillingSeen()
		go a.runUsagePoller(ctx)
		go a.runBillingExporter(ctx)
	} else {
		logf("CPA_MGMT_KEY is empty; meta/ usage polling and billing export disabled")
	}

	// Upload immediately on startup, then periodically.
	a.uploadRawOnce(ctx)
	ticker := time.NewTicker(cfg.UploadInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if cfg.ManagementKey != "" {
				a.flushUsageStale(time.Time{}, true)
				a.flushBillingStale(time.Time{}, true)
			}
			logf("stopped")
			return
		case <-ticker.C:
			a.uploadRawOnce(ctx)
		}
	}
}

func loadConfig() (config, error) {
	cfg := config{
		Bucket:          strings.TrimSpace(os.Getenv("BUCKET")),
		Region:          envDefault("AWS_REGION", envDefault("AWS_DEFAULT_REGION", "ca-central-1")),
		UseSSL:          envBool("S3_USE_SSL", true),
		LogDir:          envDefault("LOG_DIR", "/home/ubuntu/CLIProxyAPI/logs"),
		StageDir:        envDefault("STAGE_DIR", "/var/lib/cpa-trace/stage"),
		RawPrefix:       strings.Trim(envDefault("RAW_PREFIX", "raw"), "/"),
		MetaPrefix:      strings.Trim(envDefault("META_PREFIX", "meta"), "/"),
		BillingPrefix:   strings.Trim(envDefault("BILLING_PREFIX", "billing"), "/"),
		Settle:          envDuration("SETTLE", time.Minute),
		UploadInterval:  envDuration("UPLOAD_INTERVAL", 2*time.Minute),
		DiskWarnMB:      envInt64("DISK_WARN_MB", 10240),
		MaxRawFiles:     envInt("MAX_RAW_FILES", 1000),
		BaseURL:         strings.TrimRight(envDefault("CPA_BASE", "http://127.0.0.1:8317"), "/"),
		ManagementKey:   strings.TrimSpace(os.Getenv("CPA_MGMT_KEY")),
		UsageSpoolDir:   envDefault("USAGE_SPOOL", "/var/lib/cpa-trace/usage"),
		BillingSpoolDir: envDefault("BILLING_SPOOL", "/var/lib/cpa-trace/billing"),
		PollInterval:    envDuration("POLL_INTERVAL", 5*time.Second),
		PollBatch:       envInt("POLL_BATCH", 500),
	}
	cfg.Endpoint = strings.TrimSpace(os.Getenv("S3_ENDPOINT"))
	if cfg.Endpoint == "" {
		cfg.Endpoint = "s3." + cfg.Region + ".amazonaws.com"
	}
	if cfg.Bucket == "" {
		return cfg, errors.New("BUCKET is required")
	}
	if cfg.RawPrefix == "" {
		cfg.RawPrefix = "raw"
	}
	if cfg.MetaPrefix == "" {
		cfg.MetaPrefix = "meta"
	}
	if cfg.BillingPrefix == "" {
		cfg.BillingPrefix = "billing"
	}
	if cfg.Settle < 0 {
		cfg.Settle = time.Minute
	}
	if cfg.UploadInterval <= 0 {
		cfg.UploadInterval = 2 * time.Minute
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.PollBatch <= 0 {
		cfg.PollBatch = 500
	}
	if cfg.MaxRawFiles <= 0 {
		cfg.MaxRawFiles = 1000
	}
	return cfg, nil
}

func newS3Client(cfg config) (*minio.Client, error) {
	accessKey := strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID"))
	secretKey := strings.TrimSpace(os.Getenv("AWS_SECRET_ACCESS_KEY"))
	sessionToken := strings.TrimSpace(os.Getenv("AWS_SESSION_TOKEN"))
	var creds *credentials.Credentials
	if accessKey != "" && secretKey != "" {
		creds = credentials.NewStaticV4(accessKey, secretKey, sessionToken)
	} else {
		creds = credentials.NewIAM("")
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{Creds: creds, Secure: cfg.UseSSL, Region: cfg.Region})
	if err != nil {
		return nil, fmt.Errorf("create s3 client: %w", err)
	}
	return client, nil
}

func (a *agent) uploadRawOnce(ctx context.Context) {
	if err := a.uploadRaw(ctx); err != nil {
		logf("raw upload failed: %v", err)
	}
}

func (a *agent) uploadRaw(ctx context.Context) error {
	used, err := dirSizeMB(a.cfg.LogDir)
	if err == nil && a.cfg.DiskWarnMB > 0 && used > a.cfg.DiskWarnMB {
		logf("warning: log dir %dMB exceeds %dMB; upload may be lagging", used, a.cfg.DiskWarnMB)
	}

	files, err := settledLogFiles(a.cfg.LogDir, a.cfg.Settle, a.cfg.MaxRawFiles)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}

	now := time.Now().UTC()
	batch := fmt.Sprintf("%s-%s-%d", now.Format("20060102T150405Z"), a.host, os.Getpid())
	tarPath := filepath.Join(a.cfg.StageDir, batch+".tar.zst")
	key := fmt.Sprintf("%s/dt=%s/hour=%s/%s.tar.zst", a.cfg.RawPrefix, now.Format("2006-01-02"), now.Format("15"), batch)

	if err := createTarZstd(ctx, a.cfg.LogDir, files, tarPath); err != nil {
		return err
	}
	if err := a.putFile(ctx, tarPath, key, "application/zstd", ""); err != nil {
		return err
	}
	for _, name := range files {
		if err := os.Remove(filepath.Join(a.cfg.LogDir, name)); err != nil && !os.IsNotExist(err) {
			logf("remove uploaded log %s failed: %v", name, err)
		}
	}
	if err := os.Remove(tarPath); err != nil && !os.IsNotExist(err) {
		logf("remove staged tar %s failed: %v", tarPath, err)
	}
	logf("uploaded %d raw logs -> s3://%s/%s", len(files), a.cfg.Bucket, key)
	return nil
}

func settledLogFiles(dir string, settle time.Duration, maxFiles int) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read log dir: %w", err)
	}
	cutoff := time.Now().Add(-settle)
	files := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Mode().IsRegular() && info.ModTime().Before(cutoff) {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)
	if len(files) > maxFiles {
		files = files[:maxFiles]
	}
	return files, nil
}

func createTarZstd(ctx context.Context, logDir string, files []string, tarPath string) error {
	if err := os.MkdirAll(filepath.Dir(tarPath), 0o755); err != nil {
		return fmt.Errorf("create stage dir: %w", err)
	}
	args := []string{"--zstd", "-cf", tarPath, "-C", logDir}
	args = append(args, files...)
	cmd := exec.CommandContext(ctx, "tar", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(tarPath)
		return fmt.Errorf("tar logs: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (a *agent) putFile(ctx context.Context, path, key, contentType, contentEncoding string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	_, err = a.s3.PutObject(ctx, a.cfg.Bucket, key, file, info.Size(), minio.PutObjectOptions{ContentType: contentType, ContentEncoding: contentEncoding})
	if err != nil {
		return fmt.Errorf("put s3://%s/%s: %w", a.cfg.Bucket, key, err)
	}
	if _, err = a.s3.StatObject(ctx, a.cfg.Bucket, key, minio.StatObjectOptions{}); err != nil {
		return fmt.Errorf("verify s3://%s/%s: %w", a.cfg.Bucket, key, err)
	}
	return nil
}

func (a *agent) runUsagePoller(ctx context.Context) {
	logf("usage poller enabled: %s", a.cfg.BaseURL)
	a.pollUsageOnce(ctx)
	ticker := time.NewTicker(a.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.pollUsageOnce(ctx)
		}
	}
}

func (a *agent) pollUsageOnce(ctx context.Context) {
	now := time.Now().UTC()
	current := filepath.Join(a.cfg.UsageSpoolDir, fmt.Sprintf("%s-%s.ndjson", now.Format("20060102T15"), a.host))
	a.flushUsageStale(now, false)
	records, err := a.popUsage(ctx)
	if err != nil {
		logf("usage poll failed: %v", err)
		return
	}
	if len(records) == 0 {
		return
	}
	f, err := os.OpenFile(current, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logf("open usage spool failed: %v", err)
		return
	}
	for _, rec := range records {
		_, _ = f.Write(rec)
		_, _ = f.Write([]byte("\n"))
	}
	if err := f.Sync(); err != nil {
		logf("sync usage spool failed: %v", err)
	}
	if err := f.Close(); err != nil {
		logf("close usage spool failed: %v", err)
	}
}

func (a *agent) popUsage(ctx context.Context) ([][]byte, error) {
	url := fmt.Sprintf("%s/v0/management/usage-queue?count=%d", a.cfg.BaseURL, a.cfg.PollBatch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Management-Key", a.cfg.ManagementKey)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("usage queue status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var raw []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	out := make([][]byte, 0, len(raw))
	for _, item := range raw {
		var s string
		if json.Unmarshal(item, &s) == nil {
			item = json.RawMessage(s)
		}
		var obj map[string]any
		if err := json.Unmarshal(item, &obj); err != nil {
			continue
		}
		compact, err := json.Marshal(obj)
		if err == nil {
			out = append(out, compact)
		}
	}
	return out, nil
}

func (a *agent) flushUsageStale(now time.Time, includeCurrent bool) {
	entries, err := os.ReadDir(a.cfg.UsageSpoolDir)
	if err != nil {
		if !os.IsNotExist(err) {
			logf("read usage spool failed: %v", err)
		}
		return
	}
	currentPrefix := ""
	if !now.IsZero() {
		currentPrefix = now.UTC().Format("20060102T15")
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".ndjson") {
			continue
		}
		if !includeCurrent && currentPrefix != "" && strings.HasPrefix(name, currentPrefix+"-") {
			continue
		}
		if err := a.shipUsageFile(context.Background(), filepath.Join(a.cfg.UsageSpoolDir, name)); err != nil {
			logf("ship usage %s failed: %v", name, err)
		}
	}
}

func (a *agent) shipUsageFile(ctx context.Context, path string) error {
	base := filepath.Base(path)
	stamp := strings.SplitN(base, "-", 2)[0]
	if len(stamp) != len("20060102T15") {
		return fmt.Errorf("unexpected usage file name %q", base)
	}
	dt := fmt.Sprintf("%s-%s-%s", stamp[0:4], stamp[4:6], stamp[6:8])
	hour := stamp[9:11]
	gzPath := path + ".gz"
	if err := gzipFile(path, gzPath); err != nil {
		return err
	}
	key := fmt.Sprintf("%s/dt=%s/hour=%s/%s.gz", a.cfg.MetaPrefix, dt, hour, base)
	if err := a.putFile(ctx, gzPath, key, "application/x-ndjson", "gzip"); err != nil {
		return err
	}
	_ = os.Remove(path)
	_ = os.Remove(gzPath)
	logf("uploaded usage -> s3://%s/%s", a.cfg.Bucket, key)
	return nil
}

func (a *agent) runBillingExporter(ctx context.Context) {
	logf("billing exporter enabled: %s", a.cfg.BaseURL)
	a.pollBillingOnce(ctx)
	ticker := time.NewTicker(a.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.pollBillingOnce(ctx)
		}
	}
}

func (a *agent) pollBillingOnce(ctx context.Context) {
	now := time.Now().UTC()
	current := filepath.Join(a.cfg.BillingSpoolDir, fmt.Sprintf("%s-%s.ndjson", now.Format("20060102T15"), a.host))
	a.flushBillingStale(now, false)
	records, err := a.fetchBillingEvents(ctx)
	if err != nil {
		logf("billing export poll failed: %v", err)
		return
	}
	if len(records) == 0 {
		return
	}
	f, err := os.OpenFile(current, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logf("open billing spool failed: %v", err)
		return
	}
	wrote := 0
	for _, rec := range records {
		id := billingEventID(rec)
		if id == "" {
			continue
		}
		if _, ok := a.billingSeen[id]; ok {
			continue
		}
		if _, errWrite := f.Write(rec); errWrite != nil {
			logf("write billing spool failed: %v", errWrite)
			break
		}
		_, _ = f.Write([]byte("\n"))
		a.billingSeen[id] = struct{}{}
		a.persistBillingSeen(id)
		wrote++
	}
	if err := f.Sync(); err != nil {
		logf("sync billing spool failed: %v", err)
	}
	if err := f.Close(); err != nil {
		logf("close billing spool failed: %v", err)
	}
	if wrote > 0 {
		logf("spooled %d billing event(s)", wrote)
	}
}

func (a *agent) fetchBillingEvents(ctx context.Context) ([][]byte, error) {
	url := a.cfg.BaseURL + "/v0/management/billing/usage/export"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Management-Key", a.cfg.ManagementKey)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("billing export status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var raw []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	out := make([][]byte, 0, len(raw))
	for _, item := range raw {
		var obj map[string]any
		if err := json.Unmarshal(item, &obj); err != nil {
			continue
		}
		compact, err := json.Marshal(obj)
		if err == nil {
			out = append(out, compact)
		}
	}
	return out, nil
}

func billingEventID(raw []byte) string {
	var obj struct {
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	return strings.TrimSpace(obj.EventID)
}

func (a *agent) billingSeenPath() string { return filepath.Join(a.cfg.BillingSpoolDir, ".seen") }

func (a *agent) loadBillingSeen() {
	data, err := os.ReadFile(a.billingSeenPath())
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		id := strings.TrimSpace(line)
		if id != "" {
			a.billingSeen[id] = struct{}{}
		}
	}
}

func (a *agent) persistBillingSeen(id string) {
	if id == "" {
		return
	}
	f, err := os.OpenFile(a.billingSeenPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logf("open billing seen failed: %v", err)
		return
	}
	_, _ = f.WriteString(id + "\n")
	_ = f.Close()
}

func (a *agent) flushBillingStale(now time.Time, includeCurrent bool) {
	entries, err := os.ReadDir(a.cfg.BillingSpoolDir)
	if err != nil {
		if !os.IsNotExist(err) {
			logf("read billing spool failed: %v", err)
		}
		return
	}
	currentPrefix := ""
	if !now.IsZero() {
		currentPrefix = now.UTC().Format("20060102T15")
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".ndjson") {
			continue
		}
		if !includeCurrent && currentPrefix != "" && strings.HasPrefix(name, currentPrefix+"-") {
			continue
		}
		if err := a.shipBillingFile(context.Background(), filepath.Join(a.cfg.BillingSpoolDir, name)); err != nil {
			logf("ship billing %s failed: %v", name, err)
		}
	}
}

func (a *agent) shipBillingFile(ctx context.Context, path string) error {
	base := filepath.Base(path)
	stamp := strings.SplitN(base, "-", 2)[0]
	if len(stamp) != len("20060102T15") {
		return fmt.Errorf("unexpected billing file name %q", base)
	}
	dt := fmt.Sprintf("%s-%s-%s", stamp[0:4], stamp[4:6], stamp[6:8])
	hour := stamp[9:11]
	gzPath := path + ".gz"
	if err := gzipFile(path, gzPath); err != nil {
		return err
	}
	key := fmt.Sprintf("%s/dt=%s/hour=%s/%s.gz", a.cfg.BillingPrefix, dt, hour, base)
	if err := a.putFile(ctx, gzPath, key, "application/x-ndjson", "gzip"); err != nil {
		return err
	}
	_ = os.Remove(path)
	_ = os.Remove(gzPath)
	logf("uploaded billing -> s3://%s/%s", a.cfg.Bucket, key)
	return nil
}

func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	gz, err := gzip.NewWriterLevel(out, gzip.BestSpeed)
	if err != nil {
		_ = out.Close()
		return err
	}
	_, copyErr := io.Copy(gz, in)
	closeGzErr := gz.Close()
	closeOutErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeGzErr != nil {
		return closeGzErr
	}
	return closeOutErr
}

func dirSizeMB(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total / (1024 * 1024), err
}

func envDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt64(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	if d, err := time.ParseDuration(value); err == nil {
		return d
	}
	seconds, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stdout, time.Now().UTC().Format(time.RFC3339)+" "+format+"\n", args...)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "cpa-trace-agent:", err)
	os.Exit(1)
}
