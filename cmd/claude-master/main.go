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
	"strings"
	"syscall"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/claudemaster"
	log "github.com/sirupsen/logrus"
)

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
		return 2, errors.New("usage: claude-master check; claude-master login PROFILE --provider claude|codex; claude-master probe PROFILE --model MODEL; claude-master run PROFILE --model MODEL [--diagnostics] -- [Claude arguments]")
	}
	command, name := args[0], args[1]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	provider := flags.String("provider", "", "inference login provider")
	model := flags.String("model", "", "selected backend model")
	diagnostics := flags.Bool("diagnostics", false, "print numeric proxy counters only")
	if err := flags.Parse(args[2:]); err != nil {
		return 2, errors.New("invalid launcher arguments")
	}
	if command != "login" && command != "run" && command != "probe" {
		return 2, errors.New("expected login, run, or probe")
	}
	if command == "login" && (*diagnostics || *model != "" || len(flags.Args()) != 0 || (*provider != "claude" && *provider != "codex")) {
		return 2, errors.New("login requires --provider claude or --provider codex, without model or Claude arguments")
	}
	if (command == "run" || command == "probe") && (*provider != "" || strings.TrimSpace(*model) == "") {
		return 2, errors.New("run and probe require --model MODEL; provider comes from the selected profile")
	}
	if command == "probe" && (*diagnostics || len(flags.Args()) != 0) {
		return 2, errors.New("probe does not accept Claude arguments or proxy diagnostics")
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
		if err := profileLock.Login(ctx, *provider, prompt); err != nil {
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
		result, err := claudemaster.Probe(ctx, profile, *model)
		if err != nil {
			return 1, err
		}
		fmt.Fprintf(os.Stdout, "Inference probe: status=%d matched=%t stage=%s\n", result.Status, result.Matched, result.Stage)
		if !result.Matched {
			return 1, errors.New("selected inference profile did not return the expected probe response")
		}
		return 0, nil
	}
	var diagnosticOutput io.Writer
	if *diagnostics {
		diagnosticOutput = os.Stderr
	}
	return claudemaster.LaunchWithDiagnostics(ctx, profile, *model, flags.Args(), diagnosticOutput)
}
