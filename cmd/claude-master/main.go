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
	"github.com/router-for-me/CLIProxyAPI/v7/internal/claudemaster"
	log "github.com/sirupsen/logrus"
)

type stringListFlag []string

func (f *stringListFlag) String() string { return strings.Join(*f, ",") }
func (f *stringListFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(args) == 1 && args[0] == "check" {
		if _, err := claudemaster.Preflight(ctx, nil); err != nil {
			return 1, err
		}
		fmt.Fprintln(os.Stdout, "Startup checks passed: native Claude "+claudemaster.NativeClaudeVersion+" and local settings. No login or session was started.")
		return 0, nil
	}
	if len(args) < 2 {
		return 2, errors.New("usage: claude-master check; claude-master login PROFILE; claude-master probe PROFILE --model MODEL; claude-master run PROFILE [--next-profile PROFILE ...] [--diagnostics] -- [Claude arguments]")
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
	switch command {
	case "probe":
		flags.StringVar(&model, "model", "", "diagnostic model")
	case "run":
		flags.BoolVar(&diagnostics, "diagnostics", false, "print numeric proxy counters only")
		flags.Var(&nextProfiles, "next-profile", "next inference profile after the preceding subscription is drained")
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
		return claudemaster.LaunchProfilesWithDiagnostics(ctx, profiles, flags.Args(), diagnosticOutput)
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
