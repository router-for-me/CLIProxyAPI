package clawmaster

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

// agentService is what keeps the agent running for this user: launchd on macOS, Task Scheduler on
// Windows. Each starts the agent at logon and again if it exits.
type agentService interface {
	// describe names the service for messages.
	describe() string
	// definitionPath is the file that defines the service (the plist, the task's XML).
	definitionPath() string
	// write defines the service that runs args.
	write(args []string) error
	// stop stops a running agent; it is not an error when there is none, but it is when the
	// service cannot tell or will not stop.
	stop(ctx context.Context) error
	// start registers the written definition and starts it now.
	start(ctx context.Context) error
	// remove unregisters the service (the definition file is removed by the caller).
	remove(ctx context.Context) error
}

// launchdService is a launchd job in the user's GUI domain.
type launchdService struct {
	home      string
	logFile   string
	launchctl func(ctx context.Context, args ...string) error
}

func (s launchdService) describe() string { return "launchd job " + agentLaunchLabel }

func (s launchdService) definitionPath() string {
	return filepath.Join(s.home, "Library", "LaunchAgents", agentLaunchLabel+".plist")
}

func (s launchdService) domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func (s launchdService) write(args []string) error {
	plist, err := agentPlist(args, strings.TrimSuffix(s.logFile, ".log")+".stderr.log")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.definitionPath(), plist, 0o644)
}

func (s launchdService) stop(ctx context.Context) error {
	service := s.domain() + "/" + agentLaunchLabel
	if err := s.launchctl(ctx, "print", service); errors.Is(err, errLaunchdNoService) {
		return nil // not loaded
	} else if err != nil {
		return fmt.Errorf("launchctl could not tell whether the agent is running: %w", err)
	}
	if err := s.launchctl(ctx, "bootout", service); err != nil {
		return fmt.Errorf("launchctl could not stop the running agent: %w", err)
	}
	return nil
}

func (s launchdService) start(ctx context.Context) error {
	if err := s.launchctl(ctx, "bootstrap", s.domain(), s.definitionPath()); err != nil {
		return fmt.Errorf("launchctl could not start the agent: %w", err)
	}
	return nil
}

// remove: a booted-out job is gone from launchd; only its plist is left, for the caller.
func (s launchdService) remove(context.Context) error { return nil }

// agentTaskName is the agent's Task Scheduler task, in the root folder.
const agentTaskName = "claw-master agent"

// errTaskNotFound is schtasks saying there is no such task.
var errTaskNotFound = errors.New("Task Scheduler has no such task")

// scheduledTaskService is a Task Scheduler task that runs at this user's logon, as this user, with
// the user's normal rights. A logon trigger limited to oneself needs no administrator (schtasks
// /sc onlogon, for any user, does), and restart-on-failure plays the part of launchd's KeepAlive.
type scheduledTaskService struct {
	dir      string // %LOCALAPPDATA%\claw-master
	userSID  string
	schtasks func(ctx context.Context, args ...string) (string, error)
}

func (s scheduledTaskService) describe() string {
	return "scheduled task " + strconv.Quote(agentTaskName)
}

func (s scheduledTaskService) definitionPath() string {
	return filepath.Join(s.dir, "agent-task.xml")
}

func (s scheduledTaskService) write(args []string) error {
	task, err := agentTaskXML(s.userSID, args)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.definitionPath(), task, 0o644)
}

// stop ends the task if it exists. /end succeeds whether or not it is running (its status text is
// localized, so it is not read), and ends conhost, which ends the agent (--exit-with-parent).
func (s scheduledTaskService) stop(ctx context.Context) error {
	if _, err := s.schtasks(ctx, "/query", "/tn", agentTaskName); errors.Is(err, errTaskNotFound) {
		return nil
	} else if err != nil {
		return fmt.Errorf("schtasks could not tell whether the agent is installed: %w", err)
	}
	if _, err := s.schtasks(ctx, "/end", "/tn", agentTaskName); err != nil {
		return fmt.Errorf("schtasks could not stop the running agent: %w", err)
	}
	return nil
}

func (s scheduledTaskService) start(ctx context.Context) error {
	if _, err := s.schtasks(ctx, "/create", "/tn", agentTaskName, "/xml", s.definitionPath(), "/f"); err != nil {
		return fmt.Errorf("schtasks could not register the agent: %w", err)
	}
	if _, err := s.schtasks(ctx, "/run", "/tn", agentTaskName); err != nil {
		return fmt.Errorf("schtasks could not start the agent: %w", err)
	}
	return nil
}

func (s scheduledTaskService) remove(ctx context.Context) error {
	if _, err := s.schtasks(ctx, "/delete", "/tn", agentTaskName, "/f"); err != nil && !errors.Is(err, errTaskNotFound) {
		return fmt.Errorf("schtasks could not remove the agent's task: %w", err)
	}
	return nil
}

// agentTaskXML is a Task Scheduler definition (UTF-16, as schtasks /xml reads it) that runs args in
// this user's session at their logon, without a console window (conhost --headless), with no time
// limit, and again a minute after it fails.
func agentTaskXML(userSID string, args []string) ([]byte, error) {
	if userSID == "" {
		return nil, errors.New("cannot tell this user's SID")
	}
	var b bytes.Buffer
	esc := func(s string) string {
		var e bytes.Buffer
		_ = xml.EscapeText(&e, []byte(s))
		return e.String()
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>claw-master agent: Claude Code's proxy to the claw-master server</Description></RegistrationInfo>
  <Triggers><LogonTrigger><Enabled>true</Enabled><UserId>` + esc(userSID) + `</UserId></LogonTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>` + esc(userSID) + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure><Interval>PT1M</Interval><Count>999</Count></RestartOnFailure>
  </Settings>
  <Actions Context="Author"><Exec>
    <Command>%SystemRoot%\System32\conhost.exe</Command>
    <Arguments>` + esc("--headless "+windowsCommandLine(args)) + `</Arguments>
  </Exec></Actions>
</Task>
`)
	units := utf16.Encode([]rune(b.String()))
	out := make([]byte, 2, 2+2*len(units))
	binary.LittleEndian.PutUint16(out, 0xFEFF)
	for _, u := range units {
		out = binary.LittleEndian.AppendUint16(out, u)
	}
	return out, nil
}

// windowsCommandLine joins args as CommandLineToArgvW splits them back: an argument with a space,
// tab or quote is quoted, its quotes escaped, and backslashes doubled where they precede a quote.
func windowsCommandLine(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		if arg != "" && !strings.ContainsAny(arg, " \t\"") {
			quoted[i] = arg
			continue
		}
		var b strings.Builder
		b.WriteByte('"')
		slashes := 0
		for _, r := range arg {
			switch r {
			case '\\':
				slashes++
				continue
			case '"':
				b.WriteString(strings.Repeat(`\`, 2*slashes+1))
			default:
				b.WriteString(strings.Repeat(`\`, slashes))
			}
			slashes = 0
			b.WriteRune(r)
		}
		b.WriteString(strings.Repeat(`\`, 2*slashes))
		b.WriteByte('"')
		quoted[i] = b.String()
	}
	return strings.Join(quoted, " ")
}
