package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/claudemaster"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

type stringListFlag []string

func (f *stringListFlag) String() string { return strings.Join(*f, ",") }
func (f *stringListFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

type modelMapFlag map[string]string

func (f *modelMapFlag) String() string {
	keys := make([]string, 0, len(*f))
	for key := range *f {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+":"+(*f)[key])
	}
	return strings.Join(pairs, ",")
}

func (f *modelMapFlag) Set(value string) error {
	if strings.Count(value, ":") != 1 {
		return errors.New("model mapping must be INCOMING:TARGET")
	}
	incoming, target, _ := strings.Cut(value, ":")
	incoming, target = strings.TrimSpace(incoming), strings.TrimSpace(target)
	if incoming == "" || target == "" {
		return errors.New("model mapping must be INCOMING:TARGET")
	}
	if _, exists := (*f)[incoming]; exists {
		return errors.New("incoming model mappings must be distinct")
	}
	if *f == nil {
		*f = make(modelMapFlag)
	}
	(*f)[incoming] = target
	return nil
}

const backupAPIKeyByteLimit = 16 * 1024

func readBackupAPIKey(source string, explicit bool) (string, string, error) {
	if !explicit {
		return readBackupAPIKeyEnv(claudemaster.BackupAPIKeyEnvironment, false)
	}
	if envName, ok := strings.CutPrefix(source, "env:"); ok {
		return readBackupAPIKeyEnv(envName, true)
	}
	path := strings.TrimPrefix(source, "file:")
	if path == "" {
		return "", "", errors.New("backup API key requires a file path or env:VARIABLE")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return "", "", errors.New("cannot read backup API key file")
	}
	file := os.NewFile(uintptr(fd), "backup-api-key")
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > backupAPIKeyByteLimit {
		return "", "", errors.New("backup API key file must be a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, backupAPIKeyByteLimit+1))
	if err != nil || len(data) > backupAPIKeyByteLimit {
		return "", "", errors.New("cannot read backup API key file")
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", "", errors.New("backup API key file must be nonempty")
	}
	if err := validateBackupAPIKey(key); err != nil {
		return "", "", err
	}
	return key, "", nil
}

func validateBackupAPIKey(key string) error {
	for _, char := range key {
		if char < '!' || char > '~' {
			return errors.New("backup API key contains invalid characters")
		}
	}
	return nil
}

func readBackupAPIKeyEnv(envName string, explicit bool) (string, string, error) {
	if err := claudemaster.ValidateBackupAPIKeyEnv(envName); err != nil {
		return "", "", err
	}
	key := strings.TrimSpace(os.Getenv(envName))
	if key == "" {
		if explicit {
			return "", "", errors.New("selected backup API key environment variable must be set and nonempty")
		}
		return "", "", nil
	}
	if len(key) > backupAPIKeyByteLimit {
		return "", "", errors.New("backup API key environment value exceeds its size limit")
	}
	if err := validateBackupAPIKey(key); err != nil {
		return "", "", err
	}
	return key, envName, nil
}

func main() {
	// Upstream SDK diagnostics can contain credential paths, response bodies, or OAuth state.
	// The launcher emits only its own fixed, sanitized errors; interactive OAuth URLs/codes are
	// intentionally shown by the authenticator only during an explicit login command.
	log.SetOutput(io.Discard)
	log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	gin.SetMode(gin.ReleaseMode)
	gin.DefaultWriter, gin.DefaultErrorWriter = io.Discard, io.Discard
	code, err := run(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "claude-master:", err.Error())
	}
	os.Exit(code)
}

func run(args []string) (int, error) {
	ctx, stop := launcherSignalContext()
	defer stop()
	if len(args) == 1 && args[0] == "check" {
		if _, err := claudemaster.Preflight(ctx, nil); err != nil {
			return 1, err
		}
		fmt.Fprintln(os.Stdout, "Startup checks passed: installed native Claude and local settings. No login or session was started.")
		return 0, nil
	}
	switch {
	case len(args) > 0 && args[0] == "account-key":
		return runAccountKey(args[1:])
	case len(args) > 0 && args[0] == "client-init":
		return runClientInit(args[1:])
	case len(args) > 0 && args[0] == "issue":
		return runIssue(args[1:])
	case len(args) > 0 && args[0] == "connect":
		return runConnect(ctx, args[1:])
	}
	if len(args) < 2 {
		return 2, errors.New("usage: claude-master check; claude-master login PROFILE; claude-master probe PROFILE --model MODEL; claude-master run PROFILE [--next-profile PROFILE ...] [--backup-api-key FILE|env:VARIABLE] [--map INCOMING:TARGET ...] [--diagnostics] -- [Claude arguments]; claude-master serve PROFILE [--next-profile PROFILE ...] --listen ADDRESS:PORT --state-dir DIR; claude-master issue --state-dir DIR --request FILE [--days N] [--out FILE]; claude-master client-init --dir DIR --name NAME; claude-master connect --server ADDRESS:PORT --dir DIR -- [Claude arguments]")
	}
	command, name := args[0], args[1]
	if command != "login" && command != "run" && command != "probe" && command != "serve" {
		return 2, errors.New("expected login, run, serve, probe, issue, client-init or connect")
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var model string
	var diagnostics bool
	var nextProfiles stringListFlag
	var backupAPIKeySource string
	var modelMap modelMapFlag
	var listen, stateDir, openLoopback string
	var balanced bool
	var drainTimeout time.Duration
	var logLevel, logFile, logFormat string
	var logMaxMB, logKeep int
	var quotaLogInterval, otlpInterval time.Duration
	var otlpEndpoint, accountLabelsFile, instance string
	var accountLabels stringListFlag
	if command == "run" || command == "serve" {
		flags.StringVar(&otlpEndpoint, "otlp-endpoint", os.Getenv("CLAUDE_MASTER_OTLP_ENDPOINT"), "export OpenTelemetry metrics to this OTLP/HTTP base URL, e.g. http://127.0.0.1:4318")
		flags.DurationVar(&otlpInterval, "otlp-interval", 30*time.Second, "how often metrics are exported")
		flags.StringVar(&instance, "instance", "", "this process's service.instance.id in metrics, so several servers stay apart (default: the hostname)")
		flags.Var(&accountLabels, "account-label", "name an incoming user's Anthropic account in metrics: ACCOUNT_UUID=NAME (repeatable; see: claude-master account-key)")
		flags.StringVar(&accountLabelsFile, "account-labels-file", "", "a file of ACCOUNT_UUID=NAME lines")
		flags.StringVar(&logLevel, "log-level", "", "debug, info, warn, error or off (default: info for serve, off for run)")
		flags.StringVar(&logFile, "log-file", "", "append logs to this file, rotated by size (run requires it: the terminal belongs to Claude)")
		flags.StringVar(&logFormat, "log-format", "text", "text or json")
		flags.IntVar(&logMaxMB, "log-max-mb", 10, "rotate the log file at this size in MiB")
		flags.IntVar(&logKeep, "log-keep", 5, "rotated log files to keep")
		flags.DurationVar(&quotaLogInterval, "quota-log-interval", 0, "how often the quota and routing snapshot is logged (default 5m; a negative value turns it off)")
	}
	switch command {
	case "serve":
		flags.Var(&nextProfiles, "next-profile", "additional inference profile for quota-aware subscription rotation")
		flags.StringVar(&backupAPIKeySource, "backup-api-key", "", "final-backup API key file path or env:VARIABLE")
		flags.Var(&modelMap, "map", "exact model mapping INCOMING:TARGET (repeatable)")
		flags.StringVar(&listen, "listen", "", "private ADDRESS:PORT to serve client boxes on")
		flags.StringVar(&stateDir, "state-dir", "", "private directory holding the server's CA")
		flags.StringVar(&openLoopback, "open-loopback", "", "also serve plain HTTP with NO client certificate on this loopback ADDRESS:PORT, for an authenticating tunnel")
		flags.BoolVar(&balanced, "balanced", false, "a load balancer in front stops sending new connections here once a rollout begins: a stopping server serves its existing connections, closing each, instead of refusing their requests")
		flags.DurationVar(&drainTimeout, "drain-timeout", 0, "how long a stopping server lets running requests finish (default 60s; keep systemd's TimeoutStopSec above it)")
	case "probe":
		flags.StringVar(&model, "model", "", "diagnostic model")
	case "run":
		flags.BoolVar(&diagnostics, "diagnostics", false, "print numeric proxy counters only")
		flags.Var(&nextProfiles, "next-profile", "additional inference profile for quota-aware subscription rotation")
		flags.StringVar(&backupAPIKeySource, "backup-api-key", "", "final-backup API key file path or env:VARIABLE")
		flags.Var(&modelMap, "map", "exact model mapping INCOMING:TARGET (repeatable)")
	}
	if err := flags.Parse(args[2:]); err != nil {
		return 2, errors.New("invalid launcher arguments")
	}
	if command == "login" && len(flags.Args()) != 0 {
		return 2, errors.New("login creates a Claude subscription profile and does not accept model or Claude arguments")
	}
	if command == "probe" && strings.TrimSpace(model) == "" {
		return 2, errors.New("probe requires --model MODEL; provider comes from the selected profile")
	}
	if command == "probe" && len(flags.Args()) != 0 {
		return 2, errors.New("probe does not accept Claude arguments or proxy diagnostics")
	}
	var backupAPIKey, consumedKeyEnv string
	if command == "run" || command == "serve" {
		level := logLevel
		if level == "" {
			level = "off"
			if command == "serve" {
				level = "info"
			}
		}
		opts := claudemaster.LogOptions{Level: level, File: logFile, Format: logFormat, MaxBytes: int64(logMaxMB) << 20, Keep: logKeep}
		if command == "serve" {
			opts.Out = os.Stderr
		}
		if lvl, _ := claudemaster.ParseLogLevel(level); lvl != nil && opts.File == "" && opts.Out == nil {
			return 2, errors.New("--log-level on run needs --log-file: the terminal belongs to Claude")
		}
		closeLog, err := claudemaster.ConfigureLogging(opts)
		if err != nil {
			return 2, err
		}
		defer closeLog()
		labels, err := loadAccountLabels(accountLabels, accountLabelsFile)
		if err != nil {
			return 2, err
		}
		stopTelemetry, err := claudemaster.StartTelemetry(claudemaster.TelemetryOptions{Endpoint: otlpEndpoint, Interval: otlpInterval, Instance: metricInstance(instance, os.Hostname), AccountLabels: labels})
		if err != nil {
			return 2, err
		}
		if command == "serve" && accountLabelsFile != "" {
			go claudemaster.WatchAccountLabels(ctx, accountLabelsFile, accountLabelsCheckInterval, func() (map[string]string, error) {
				return loadAccountLabels(accountLabels, accountLabelsFile)
			})
		}
		defer func() {
			// No deadline of ours (repository policy). If a collector hangs the final export, the service
			// manager's stop timeout ends the process.
			_ = stopTelemetry(context.Background())
		}()
	}
	if command == "serve" && (listen == "" || stateDir == "") {
		return 2, errors.New("serve requires --listen ADDRESS:PORT and --state-dir DIR")
	}
	if command == "serve" && len(flags.Args()) != 0 {
		return 2, errors.New("serve does not accept Claude arguments")
	}
	if command == "run" || command == "serve" {
		explicit := false
		flags.Visit(func(f *flag.Flag) {
			if f.Name == "backup-api-key" {
				explicit = true
			}
		})
		var err error
		backupAPIKey, consumedKeyEnv, err = readBackupAPIKey(backupAPIKeySource, explicit)
		if err != nil {
			return 2, err
		}
	}
	profileNames := append([]string{name}, nextProfiles...)
	seenProfiles := make(map[string]struct{}, len(profileNames))
	for _, profileName := range profileNames {
		if err := claudemaster.ValidateProfileName(profileName); err != nil {
			return 2, err
		}
		if _, exists := seenProfiles[profileName]; exists {
			return 2, errors.New("ordered inference profiles must be distinct")
		}
		seenProfiles[profileName] = struct{}{}
	}
	if command == "serve" {
		profiles, locks, err := openRunProfiles(profileNames)
		if err != nil {
			return 1, err
		}
		defer closeProfileLocks(locks)
		// A server that could not start must exit non-zero, or a service manager would call it a
		// success and never restart it.
		if err := claudemaster.Serve(ctx, profiles, claudemaster.ServeOptions{
			LaunchOptions: claudemaster.LaunchOptions{BackupAPIKey: backupAPIKey, BackupAPIKeyEnv: consumedKeyEnv, ModelMap: modelMap},
			Listen:        listen, StateDir: stateDir, OpenLoopback: openLoopback, Out: os.Stderr, SnapshotInterval: quotaLogInterval,
			Balanced: balanced, DrainTimeout: drainTimeout,
		}); err != nil {
			return 1, err
		}
		return 0, nil
	}
	if command == "run" {
		profiles, locks, err := openRunProfiles(profileNames)
		if err != nil {
			return 1, err
		}
		defer closeProfileLocks(locks)
		var diagnosticOutput io.Writer
		if diagnostics {
			diagnosticOutput = os.Stderr
		}
		return claudemaster.LaunchProfilesWithOptions(ctx, profiles, flags.Args(), claudemaster.LaunchOptions{
			BackupAPIKey: backupAPIKey, BackupAPIKeyEnv: consumedKeyEnv,
			ModelMap: modelMap, Diagnostics: diagnosticOutput, SnapshotInterval: quotaLogInterval,
		})
	}
	profileLock, err := claudemaster.OpenProfile(name, command == "login")
	if err != nil {
		return 1, err
	}
	defer func() { _ = profileLock.Close() }()
	if command == "login" {
		reader := bufio.NewReader(os.Stdin)
		prompt := func(label string) (string, error) {
			fmt.Fprint(os.Stderr, label)
			line, err := reader.ReadString('\n')
			if err != nil && err != io.EOF {
				return "", errors.New("cannot read OAuth callback")
			}
			return strings.TrimSpace(line), nil
		}
		if err := profileLock.Login(ctx, "claude", prompt); err != nil {
			return 1, err
		}
		fmt.Fprintln(os.Stdout, "Inference profile saved. Native master login was not changed.")
		return 0, nil
	}
	profile, err := profileLock.Profile()
	if err != nil {
		return 1, err
	}
	if command == "probe" {
		result, err := claudemaster.Probe(ctx, profile, model)
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(os.Stdout, "Inference probe: status=%d matched=%t stage=%s\n", result.Status, result.Matched, result.Stage)
		if !result.Matched {
			return 1, errors.New("selected inference profile did not return the expected probe response")
		}
		return 0, nil
	}
	return 2, errors.New("expected login, run, or probe")
}

func launcherSignalContext() (context.Context, context.CancelFunc) {
	// tmux pane/session shutdown and terminal disconnects deliver SIGHUP. Treat
	// them like Ctrl-C so refresh persistence finishes before profile locks close.
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
}

func openRunProfiles(names []string) ([]claudemaster.Profile, []*claudemaster.ProfileLock, error) {
	lockOrder := append([]string(nil), names...)
	sort.Strings(lockOrder)
	byName := make(map[string]*claudemaster.ProfileLock, len(names))
	locks := make([]*claudemaster.ProfileLock, 0, len(names))
	for _, name := range lockOrder {
		profileLock, err := claudemaster.OpenProfile(name, false)
		if err != nil {
			closeProfileLocks(locks)
			return nil, nil, err
		}
		locks = append(locks, profileLock)
		byName[name] = profileLock
	}
	profiles := make([]claudemaster.Profile, 0, len(names))
	for _, name := range names {
		profile, err := byName[name].Profile()
		if err != nil {
			closeProfileLocks(locks)
			return nil, nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, locks, nil
}

func closeProfileLocks(locks []*claudemaster.ProfileLock) {
	for i := len(locks) - 1; i >= 0; i-- {
		_ = locks[i].Close()
	}
}

// runClientInit makes this box's key and a certificate request for the server to sign.
func runClientInit(args []string) (int, error) {
	flags := flag.NewFlagSet("client-init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var dir, name string
	flags.StringVar(&dir, "dir", "", "this box's client directory")
	flags.StringVar(&name, "name", "", "this box's name (lowercase letters, digits, hyphens)")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 0 || dir == "" || name == "" {
		return 2, errors.New("usage: claude-master client-init --dir DIR --name NAME")
	}
	request, err := claudemaster.CreateClientRequest(dir, name)
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(os.Stdout, "Key made in %s (it never leaves this box). Have the server sign %s, then put the signed certificate in %s as client.pem and the server's ca.pem beside it.\n", dir, request, dir)
	return 0, nil
}

// runIssue is the server side: it signs a client's certificate request with the server's CA.
func runIssue(args []string) (int, error) {
	flags := flag.NewFlagSet("issue", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var stateDir, request, out string
	days := 30
	flags.StringVar(&stateDir, "state-dir", "", "the server's state directory")
	flags.StringVar(&request, "request", "", "the client's certificate request (client.csr)")
	flags.StringVar(&out, "out", "", "where to write the certificate (default: standard output)")
	flags.IntVar(&days, "days", days, "validity in days (1-90)")
	if err := flags.Parse(args); err != nil || len(flags.Args()) != 0 || stateDir == "" || request == "" {
		return 2, errors.New("usage: claude-master issue --state-dir DIR --request FILE [--days N] [--out FILE]")
	}
	certPEM, name, err := claudemaster.SignClientRequest(stateDir, request, days)
	if err != nil {
		return 1, err
	}
	if out == "" {
		_, _ = os.Stdout.Write(certPEM)
		return 0, nil
	}
	if err := os.WriteFile(out, certPEM, 0o644); err != nil {
		return 1, errors.New("cannot write the certificate")
	}
	fmt.Fprintf(os.Stderr, "Signed %q for %d days: %s\n", name, days, out)
	return 0, nil
}

// runConnect starts the native Claude through a claude-master server.
func runConnect(ctx context.Context, args []string) (int, error) {
	flags := flag.NewFlagSet("connect", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var server, dir, open, caFile string
	flags.StringVar(&server, "server", os.Getenv("CLAUDE_MASTER_SERVER"), "the server's private ADDRESS:PORT")
	flags.StringVar(&dir, "dir", os.Getenv("CLAUDE_MASTER_CLIENT_DIR"), "this box's client directory")
	flags.StringVar(&open, "open", os.Getenv("CLAUDE_MASTER_OPEN"), "loopback ADDRESS:PORT of an authenticating tunnel to the server's open listener (no client certificate)")
	flags.StringVar(&caFile, "ca", os.Getenv("CLAUDE_MASTER_CA"), "the server's public ca.pem (with --open)")
	if err := flags.Parse(args); err != nil || (open == "" && (server == "" || dir == "")) {
		return 2, errors.New("usage: claude-master connect --server ADDRESS:PORT --dir DIR -- [Claude arguments]; or claude-master connect --open 127.0.0.1:PORT --ca FILE -- [Claude arguments] (defaults from CLAUDE_MASTER_SERVER, CLAUDE_MASTER_CLIENT_DIR, CLAUDE_MASTER_OPEN, CLAUDE_MASTER_CA)")
	}
	return claudemaster.Connect(ctx, claudemaster.ConnectOptions{Server: server, Dir: dir, Open: open, CAFile: caFile, Out: os.Stderr}, flags.Args())
}

var accountIDPattern = regexp.MustCompile(`^[0-9a-fA-F-]{8,64}$`)

// loadAccountLabels reads ACCOUNT_UUID=NAME pairs from flags and an optional file. The UUID is only a key
// into a lookup table: metrics carry the name, never the id.
// metricInstance is the service.instance.id metrics carry: --instance when given, else the hostname,
// else none.
func metricInstance(flag string, hostname func() (string, error)) string {
	if instance := strings.TrimSpace(flag); instance != "" {
		return instance
	}
	if name, err := hostname(); err == nil {
		return strings.TrimSpace(name)
	}
	return ""
}

func loadAccountLabels(pairs []string, file string) (map[string]string, error) {
	labels := make(map[string]string)
	add := func(line string) error {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			return nil
		}
		id, name, ok := strings.Cut(line, "=")
		if !ok {
			id, name, ok = strings.Cut(line, " ")
		}
		id, name = strings.TrimSpace(id), strings.TrimSpace(name)
		if !ok || name == "" || !accountIDPattern.MatchString(id) {
			return errors.New("an account label must be ACCOUNT_UUID=NAME")
		}
		labels[strings.ToLower(id)] = name
		return nil
	}
	for _, pair := range pairs {
		if err := add(pair); err != nil {
			return nil, err
		}
	}
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, errors.New("cannot read --account-labels-file")
		}
		for _, line := range strings.Split(string(data), "\n") {
			if err := add(line); err != nil {
				return nil, err
			}
		}
	}
	return labels, nil
}

// accountLabelsCheckInterval is how often serve looks at --account-labels-file for a change.
const accountLabelsCheckInterval = time.Minute

// runAccountKey prints the key the dashboards use for an Anthropic account id, so a label can be written
// for it: claude-master account-key UUID...
func runAccountKey(args []string) (int, error) {
	if len(args) == 0 {
		return 2, errors.New("usage: claude-master account-key ACCOUNT_UUID...")
	}
	for _, id := range args {
		if !accountIDPattern.MatchString(id) {
			return 2, errors.New("an account id is hexadecimal with dashes")
		}
		fmt.Fprintf(os.Stdout, "%s  %s\n", id, claudemaster.AccountKeyFor(id))
	}
	return 0, nil
}
