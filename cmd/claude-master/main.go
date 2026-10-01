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
	"sort"
	"strings"
	"syscall"

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
	if len(args) < 2 {
		return 2, errors.New("usage: claude-master check; claude-master login PROFILE; claude-master probe PROFILE --model MODEL; claude-master run PROFILE [--next-profile PROFILE ...] [--backup-api-key FILE|env:VARIABLE] [--map INCOMING:TARGET ...] [--diagnostics] -- [Claude arguments]")
	}
	command, name := args[0], args[1]
	if command != "login" && command != "run" && command != "probe" {
		return 2, errors.New("expected login, run, or probe")
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var model string
	var diagnostics bool
	var nextProfiles stringListFlag
	var backupAPIKeySource string
	var modelMap modelMapFlag
	switch command {
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
	if command == "run" {
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
			ModelMap: modelMap, Diagnostics: diagnosticOutput,
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
