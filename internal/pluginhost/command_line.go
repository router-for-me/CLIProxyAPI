package pluginhost

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

type commandLineFlagRecord struct {
	pluginID string
	flag     pluginapi.CommandLineFlag
	value    string
	set      bool
}

// RegisterCommandLineFlags exposes plugin-declared flags on the provided FlagSet.
func (h *Host) RegisterCommandLineFlags(ctx context.Context, flagSet *flag.FlagSet) {
	if h == nil || flagSet == nil {
		return
	}

	for _, record := range h.activeRecords() {
		plugin := record.plugin.Capabilities.CommandLinePlugin
		if plugin == nil || h.isPluginFused(record.id) {
			continue
		}
		resp, errRegister := h.callCommandLineRegistrar(ctx, record, plugin)
		if errRegister != nil {
			log.Warnf("pluginhost: command-line registrar %s failed: %v", record.id, errRegister)
			continue
		}
		for _, item := range resp.Flags {
			h.registerCommandLineFlag(flagSet, record.id, item)
		}
	}
}

func (h *Host) callCommandLineRegistrar(ctx context.Context, record capabilityRecord, plugin pluginapi.CommandLinePlugin) (resp pluginapi.CommandLineRegistrationResponse, err error) {
	if h == nil || plugin == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.CommandLineRegistrationResponse{}, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "CommandLinePlugin.RegisterCommandLine", recovered)
			resp = pluginapi.CommandLineRegistrationResponse{}
			err = fmt.Errorf("command-line registrar panic: %v", recovered)
		}
	}()
	return plugin.RegisterCommandLine(ctx, pluginapi.CommandLineRegistrationRequest{Plugin: record.meta})
}

func (h *Host) registerCommandLineFlag(flagSet *flag.FlagSet, pluginID string, item pluginapi.CommandLineFlag) {
	name := strings.TrimSpace(item.Name)
	if !validCommandLineFlagName(name) {
		log.Warnf("pluginhost: plugin %s declared invalid command-line flag %q", pluginID, item.Name)
		return
	}
	kind := normalizeCommandLineFlagType(item.Type)
	if kind == "" {
		log.Warnf("pluginhost: plugin %s declared unsupported command-line flag type %q for %s", pluginID, item.Type, name)
		return
	}
	value, okDefault := normalizeCommandLineFlagValue(kind, item.DefaultValue)
	if !okDefault {
		log.Warnf("pluginhost: plugin %s declared invalid default value %q for %s", pluginID, item.DefaultValue, name)
		return
	}
	if flagSet.Lookup(name) != nil {
		log.Warnf("pluginhost: plugin %s command-line flag %s conflicts with an existing flag and was skipped", pluginID, name)
		return
	}

	h.mu.Lock()
	if _, exists := h.commandLineFlags[name]; exists {
		h.mu.Unlock()
		log.Warnf("pluginhost: plugin %s command-line flag %s conflicts with a higher-priority plugin and was skipped", pluginID, name)
		return
	}
	h.commandLineFlags[name] = commandLineFlagRecord{
		pluginID: pluginID,
		flag: pluginapi.CommandLineFlag{
			Name:         name,
			Usage:        item.Usage,
			Type:         kind,
			DefaultValue: value,
		},
		value: value,
	}
	h.mu.Unlock()

	flagSet.Var(&commandLineFlagValue{
		host: h,
		name: name,
		kind: kind,
	}, name, item.Usage)
}

func validCommandLineFlagName(name string) bool {
	return name != "" &&
		!strings.HasPrefix(name, "-") &&
		name != "help" &&
		name != "h" &&
		!strings.ContainsAny(name, " \t\r\n=")
}

func normalizeCommandLineFlagType(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "bool":
		return "bool"
	case "string":
		return "string"
	case "int":
		return "int"
	case "int64":
		return "int64"
	case "float64":
		return "float64"
	case "duration":
		return "duration"
	default:
		return ""
	}
}

func normalizeCommandLineFlagValue(kind, value string) (string, bool) {
	switch kind {
	case "bool":
		if strings.TrimSpace(value) == "" {
			return "false", true
		}
		parsed, errParse := strconv.ParseBool(value)
		if errParse != nil {
			return "", false
		}
		return strconv.FormatBool(parsed), true
	case "string":
		return value, true
	case "int":
		if strings.TrimSpace(value) == "" {
			return "0", true
		}
		parsed, errParse := strconv.Atoi(value)
		if errParse != nil {
			return "", false
		}
		return strconv.Itoa(parsed), true
	case "int64":
		if strings.TrimSpace(value) == "" {
			return "0", true
		}
		parsed, errParse := strconv.ParseInt(value, 10, 64)
		if errParse != nil {
			return "", false
		}
		return strconv.FormatInt(parsed, 10), true
	case "float64":
		if strings.TrimSpace(value) == "" {
			return "0", true
		}
		parsed, errParse := strconv.ParseFloat(value, 64)
		if errParse != nil {
			return "", false
		}
		return strconv.FormatFloat(parsed, 'g', -1, 64), true
	case "duration":
		if strings.TrimSpace(value) == "" {
			return "0s", true
		}
		parsed, errParse := time.ParseDuration(value)
		if errParse != nil {
			return "", false
		}
		return parsed.String(), true
	default:
		return "", false
	}
}

type commandLineFlagValue struct {
	host *Host
	name string
	kind string
}

func (v *commandLineFlagValue) String() string {
	if v == nil || v.host == nil {
		return ""
	}
	v.host.mu.Lock()
	defer v.host.mu.Unlock()
	return v.host.commandLineFlags[v.name].value
}

func (v *commandLineFlagValue) Set(raw string) error {
	if v == nil || v.host == nil {
		return nil
	}
	normalized, okValue := normalizeCommandLineFlagValue(v.kind, raw)
	if !okValue {
		return fmt.Errorf("invalid %s value %q", v.kind, raw)
	}
	v.host.mu.Lock()
	record, okRecord := v.host.commandLineFlags[v.name]
	if okRecord {
		record.value = normalized
		record.set = true
		v.host.commandLineFlags[v.name] = record
		v.host.commandLineHits[v.name] = struct{}{}
	}
	v.host.mu.Unlock()
	return nil
}

func (v *commandLineFlagValue) IsBoolFlag() bool {
	return v != nil && v.kind == "bool"
}

// HasTriggeredCommandLineFlags reports whether any plugin-owned flag was provided.
func (h *Host) HasTriggeredCommandLineFlags() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.commandLineHits) > 0
}

// ExecuteCommandLine runs all enabled plugins whose command-line flags were provided.
func (h *Host) ExecuteCommandLine(ctx context.Context, program string, args []string, configPath string, flagSet *flag.FlagSet) (int, bool) {
	if h == nil {
		return 0, false
	}

	triggeredByPlugin, allFlags := h.commandLineExecutionState(flagSet)
	if len(triggeredByPlugin) == 0 {
		return 0, false
	}

	exitCode := 0
	handled := false
	for _, record := range h.activeRecords() {
		plugin := record.plugin.Capabilities.CommandLinePlugin
		if plugin == nil || h.isPluginFused(record.id) {
			continue
		}
		triggered := triggeredByPlugin[record.id]
		if len(triggered) == 0 {
			continue
		}
		handled = true
		resp, errExecute := h.callCommandLineExecutor(ctx, record, plugin, pluginapi.CommandLineExecutionRequest{
			Plugin:         record.meta,
			Program:        program,
			Args:           append([]string(nil), args...),
			ConfigPath:     configPath,
			Host:           h.hostConfigSummary(),
			Flags:          cloneCommandLineFlagValues(allFlags),
			TriggeredFlags: cloneCommandLineFlagValues(triggered),
		})
		if errExecute != nil {
			log.Warnf("pluginhost: command-line plugin %s failed: %v", record.id, errExecute)
			if exitCode == 0 {
				exitCode = 1
			}
			continue
		}
		if resp.ExitCode == 0 && len(resp.Auths) > 0 {
			savedPaths, errPersist := h.persistCommandLineAuths(ctx, resp.Auths)
			if errPersist != nil {
				writeCommandLineOutput(os.Stdout, resp.Stdout)
				writeCommandLineOutput(os.Stderr, resp.Stderr)
				writeCommandLineOutput(os.Stderr, []byte(errPersist.Error()+"\n"))
				if exitCode == 0 {
					exitCode = 1
				}
				continue
			}
			resp.Stdout = appendCommandLineSavedPaths(resp.Stdout, savedPaths)
		}
		writeCommandLineOutput(os.Stdout, resp.Stdout)
		writeCommandLineOutput(os.Stderr, resp.Stderr)
		if resp.ExitCode != 0 && exitCode == 0 {
			exitCode = resp.ExitCode
		}
	}
	return exitCode, handled
}

func (h *Host) commandLineExecutionState(flagSet *flag.FlagSet) (map[string]map[string]pluginapi.CommandLineFlagValue, map[string]pluginapi.CommandLineFlagValue) {
	triggeredByPlugin := make(map[string]map[string]pluginapi.CommandLineFlagValue)
	allFlags := make(map[string]pluginapi.CommandLineFlagValue)
	setFlags := make(map[string]struct{})
	if flagSet != nil {
		flagSet.Visit(func(f *flag.Flag) {
			setFlags[f.Name] = struct{}{}
		})
		flagSet.VisitAll(func(f *flag.Flag) {
			allFlags[f.Name] = pluginapi.CommandLineFlagValue{
				Name:  f.Name,
				Type:  "",
				Value: f.Value.String(),
				Set:   false,
			}
		})
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for name, record := range h.commandLineFlags {
		value := pluginapi.CommandLineFlagValue{
			Name:  name,
			Type:  record.flag.Type,
			Value: record.value,
			Set:   record.set,
		}
		if _, set := setFlags[name]; set {
			value.Set = true
		}
		allFlags[name] = value
		if _, hit := h.commandLineHits[name]; !hit {
			continue
		}
		if triggeredByPlugin[record.pluginID] == nil {
			triggeredByPlugin[record.pluginID] = make(map[string]pluginapi.CommandLineFlagValue)
		}
		triggeredByPlugin[record.pluginID][name] = value
	}
	return triggeredByPlugin, allFlags
}

func cloneCommandLineFlagValues(in map[string]pluginapi.CommandLineFlagValue) map[string]pluginapi.CommandLineFlagValue {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]pluginapi.CommandLineFlagValue, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func (h *Host) callCommandLineExecutor(ctx context.Context, record capabilityRecord, plugin pluginapi.CommandLinePlugin, req pluginapi.CommandLineExecutionRequest) (resp pluginapi.CommandLineExecutionResponse, err error) {
	if h == nil || plugin == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.CommandLineExecutionResponse{}, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "CommandLinePlugin.ExecuteCommandLine", recovered)
			resp = pluginapi.CommandLineExecutionResponse{}
			err = fmt.Errorf("command-line execution panic: %v", recovered)
		}
	}()
	return plugin.ExecuteCommandLine(ctx, req)
}

func (h *Host) persistCommandLineAuths(ctx context.Context, auths []pluginapi.AuthData) ([]string, error) {
	if len(auths) == 0 {
		return nil, nil
	}
	store := sdkAuth.GetTokenStore()
	if store == nil {
		return nil, fmt.Errorf("pluginhost: token store unavailable")
	}
	summary := h.hostConfigSummary()
	if summary.AuthDir != "" {
		if setter, okSetter := store.(interface{ SetBaseDir(string) }); okSetter {
			setter.SetBaseDir(summary.AuthDir)
		}
	}
	savedPaths := make([]string, 0, len(auths))
	for index, authData := range auths {
		record := h.AuthDataToCoreAuth(authData, "", "")
		if record == nil {
			return savedPaths, fmt.Errorf("pluginhost: command-line auth %d is invalid", index+1)
		}
		savedPath, errSave := store.Save(ctx, record)
		if errSave != nil {
			return savedPaths, fmt.Errorf("pluginhost: save command-line auth %s: %w", record.ID, errSave)
		}
		if strings.TrimSpace(savedPath) != "" {
			savedPaths = append(savedPaths, savedPath)
		}
	}
	return savedPaths, nil
}

func appendCommandLineSavedPaths(stdout []byte, savedPaths []string) []byte {
	if len(savedPaths) == 0 {
		return stdout
	}
	out := append([]byte(nil), stdout...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	for _, savedPath := range savedPaths {
		if strings.TrimSpace(savedPath) == "" {
			continue
		}
		out = append(out, []byte(fmt.Sprintf("Authentication saved to %s\n", savedPath))...)
	}
	return out
}

func writeCommandLineOutput(w io.Writer, data []byte) {
	if w == nil || len(data) == 0 {
		return
	}
	if _, errWrite := w.Write(data); errWrite != nil {
		log.Warnf("pluginhost: failed to write command-line plugin output: %v", errWrite)
	}
}
