package logging

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	ini "gopkg.in/ini.v1"
)

const LoggingConfigFileName = "logging.ini"

type RuntimeLogPolicy struct {
	RotateDaily       bool
	CompressAfterDays int
	DeleteAfterDays   int
}

type RequestLogPolicy struct {
	AggregateDaily    bool
	CompressAfterDays int
	DeleteAfterDays   int
}

type FileLogPolicy struct {
	Runtime RuntimeLogPolicy
	Request RequestLogPolicy
}

var (
	fileLogPolicyMu    sync.Mutex
	fileLogPolicyCache = make(map[string]FileLogPolicy)
)

func DefaultFileLogPolicy() FileLogPolicy {
	return FileLogPolicy{
		Runtime: RuntimeLogPolicy{
			RotateDaily:       true,
			CompressAfterDays: 1,
			DeleteAfterDays:   30,
		},
		Request: RequestLogPolicy{
			AggregateDaily:    true,
			CompressAfterDays: 1,
			DeleteAfterDays:   30,
		},
	}
}

func ResolveLoggingConfigPath(configFilePath string) string {
	return ResolveLoggingConfigPathFromDir(filepath.Dir(strings.TrimSpace(configFilePath)))
}

func ResolveLoggingConfigPathFromDir(configDir string) string {
	trimmed := strings.TrimSpace(configDir)
	if trimmed == "" {
		if wd, err := os.Getwd(); err == nil {
			trimmed = wd
		}
	}
	if trimmed == "" {
		return LoggingConfigFileName
	}
	return filepath.Join(trimmed, LoggingConfigFileName)
}

func LoadFileLogPolicy(configFilePath string) (FileLogPolicy, error) {
	return loadFileLogPolicyByPath(ResolveLoggingConfigPath(configFilePath))
}

func LoadFileLogPolicyFromDir(configDir string) (FileLogPolicy, error) {
	return loadFileLogPolicyByPath(ResolveLoggingConfigPathFromDir(configDir))
}

func loadFileLogPolicyByPath(path string) (FileLogPolicy, error) {
	policyPath := filepath.Clean(strings.TrimSpace(path))
	defaults := DefaultFileLogPolicy()
	if policyPath == "." || policyPath == "" {
		return defaults, nil
	}

	policy, err := parseFileLogPolicy(policyPath)

	fileLogPolicyMu.Lock()
	defer fileLogPolicyMu.Unlock()

	if err == nil {
		fileLogPolicyCache[policyPath] = policy
		return policy, nil
	}

	if cached, ok := fileLogPolicyCache[policyPath]; ok {
		return cached, err
	}

	fileLogPolicyCache[policyPath] = defaults
	if errors.Is(err, os.ErrNotExist) {
		return defaults, nil
	}
	return defaults, err
}

func parseFileLogPolicy(path string) (FileLogPolicy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return FileLogPolicy{}, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return FileLogPolicy{}, fmt.Errorf("logging config %s is empty", path)
	}

	cfg, err := ini.LoadSources(ini.LoadOptions{
		Insensitive:         true,
		IgnoreInlineComment: true,
	}, path)
	if err != nil {
		return FileLogPolicy{}, err
	}

	policy := DefaultFileLogPolicy()
	runtimeSec := cfg.Section("runtime")
	requestSec := cfg.Section("request")

	policy.Runtime.RotateDaily = runtimeSec.Key("rotate_daily").MustBool(policy.Runtime.RotateDaily)
	policy.Runtime.CompressAfterDays = runtimeSec.Key("compress_after_days").MustInt(policy.Runtime.CompressAfterDays)
	policy.Runtime.DeleteAfterDays = runtimeSec.Key("delete_after_days").MustInt(policy.Runtime.DeleteAfterDays)

	policy.Request.AggregateDaily = requestSec.Key("aggregate_daily").MustBool(policy.Request.AggregateDaily)
	policy.Request.CompressAfterDays = requestSec.Key("compress_after_days").MustInt(policy.Request.CompressAfterDays)
	policy.Request.DeleteAfterDays = requestSec.Key("delete_after_days").MustInt(policy.Request.DeleteAfterDays)

	sanitizeFileLogPolicy(&policy)
	return policy, nil
}

func sanitizeFileLogPolicy(policy *FileLogPolicy) {
	if policy == nil {
		return
	}
	if policy.Runtime.CompressAfterDays < 1 {
		policy.Runtime.CompressAfterDays = 1
	}
	if policy.Runtime.DeleteAfterDays < policy.Runtime.CompressAfterDays {
		policy.Runtime.DeleteAfterDays = 30
		if policy.Runtime.DeleteAfterDays < policy.Runtime.CompressAfterDays {
			policy.Runtime.DeleteAfterDays = policy.Runtime.CompressAfterDays
		}
	}
	if policy.Request.CompressAfterDays < 1 {
		policy.Request.CompressAfterDays = 1
	}
	if policy.Request.DeleteAfterDays < policy.Request.CompressAfterDays {
		policy.Request.DeleteAfterDays = 30
		if policy.Request.DeleteAfterDays < policy.Request.CompressAfterDays {
			policy.Request.DeleteAfterDays = policy.Request.CompressAfterDays
		}
	}
}

func IsLoggingConfigMissingErr(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}
