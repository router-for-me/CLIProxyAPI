package common

import (
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/constant"
)

var (
	Port         = flag.Int("port", 3000, "the listening port")
	PrintVersion = flag.Bool("version", false, "print version and exit")
	PrintHelp    = flag.Bool("help", false, "print help and exit")
	LogDir       = flag.String("log-dir", "./logs", "specify the log directory")
)

func printHelp() {
	fmt.Println("NewAPI(Based OneAPI) " + Version + " - The next-generation LLM gateway and AI asset management system supports multiple languages.")
	fmt.Println("Original Project: OneAPI by JustSong - https://github.com/songquanpeng/one-api")
	fmt.Println("Maintainer: QuantumNous - https://github.com/QuantumNous/new-api")
	fmt.Println("Usage: newapi [--port <port>] [--log-dir <log directory>] [--version] [--help]")
}

func InitEnv() {
	flag.Parse()

	envVersion := os.Getenv("VERSION")
	if envVersion != "" {
		Version = envVersion
	}

	if *PrintVersion {
		fmt.Println(Version)
		os.Exit(0)
	}

	if *PrintHelp {
		printHelp()
		os.Exit(0)
	}

	if os.Getenv("SESSION_SECRET") != "" {
		ss := os.Getenv("SESSION_SECRET")
		if ss == "random_string" {
			log.Println("WARNING: SESSION_SECRET is set to the default value 'random_string', please change it to a random string.")
			log.Println("警告：SESSION_SECRET被设置为默认值'random_string'，请修改为随机字符串。")
			log.Fatal("Please set SESSION_SECRET to a random string.")
		} else {
			SessionSecret = ss
		}
	}
	if os.Getenv("CRYPTO_SECRET") != "" {
		CryptoSecret = os.Getenv("CRYPTO_SECRET")
	} else {
		CryptoSecret = SessionSecret
	}
	if err := InitSessionCookieSettings(); err != nil {
		log.Fatal(err)
	}
	initUserSessionSettings()
	if os.Getenv("SQLITE_PATH") != "" {
		SQLitePath = os.Getenv("SQLITE_PATH")
	}
	if *LogDir != "" {
		var err error
		*LogDir, err = filepath.Abs(*LogDir)
		if err != nil {
			log.Fatal(err)
		}
		if _, err := os.Stat(*LogDir); os.IsNotExist(err) {
			err = os.Mkdir(*LogDir, 0777)
			if err != nil {
				log.Fatal(err)
			}
		}
	}

	// Initialize variables from constants.go that were using environment variables
	DebugEnabled = os.Getenv("DEBUG") == "true"
	MemoryCacheEnabled = os.Getenv("MEMORY_CACHE_ENABLED") == "true"
	IsMasterNode = os.Getenv("NODE_TYPE") != "slave"
	PasswordLoginEncryptionEnabled = GetEnvOrDefaultBool("PASSWORD_LOGIN_ENCRYPTION_ENABLED", false)
	initNodeNameIdentity()
	TLSInsecureSkipVerify = GetEnvOrDefaultBool("TLS_INSECURE_SKIP_VERIFY", false)
	if TLSInsecureSkipVerify {
		if tr, ok := http.DefaultTransport.(*http.Transport); ok && tr != nil {
			if tr.TLSClientConfig != nil {
				tr.TLSClientConfig.InsecureSkipVerify = true
			} else {
				tr.TLSClientConfig = InsecureTLSConfig
			}
		}
	}
	SMTPStartTLSEnabled = GetEnvOrDefaultBool("SMTP_STARTTLS_ENABLE", GetEnvOrDefaultBool("SMTP_STARTTLS_ENABLED", false))
	SMTPInsecureSkipVerify = GetEnvOrDefaultBool("SMTP_INSECURE_SKIP_VERIFY", GetEnvOrDefaultBool("SMTP_TLS_INSECURE_SKIP_VERIFY", false))

	// Parse requestInterval and set RequestInterval
	requestInterval, _ = strconv.Atoi(os.Getenv("POLLING_INTERVAL"))
	RequestInterval = time.Duration(requestInterval) * time.Second

	// Initialize variables with GetEnvOrDefault
	SyncFrequency = GetEnvOrDefault("SYNC_FREQUENCY", 60)
	BatchUpdateInterval = GetEnvOrDefault("BATCH_UPDATE_INTERVAL", 5)
	RelayTimeout = GetEnvOrDefault("RELAY_TIMEOUT", 0)
	RelayIdleConnTimeout = GetEnvOrDefault("RELAY_IDLE_CONN_TIMEOUT", 90)
	RelayResponseHeaderTimeout = GetEnvOrDefault("RELAY_RESPONSE_HEADER_TIMEOUT", 1800)
	RelayMaxIdleConns = GetEnvOrDefault("RELAY_MAX_IDLE_CONNS", 500)
	RelayMaxIdleConnsPerHost = GetEnvOrDefault("RELAY_MAX_IDLE_CONNS_PER_HOST", 100)

	// Initialize string variables with GetEnvOrDefaultString
	GeminiSafetySetting = GetEnvOrDefaultString("GEMINI_SAFETY_SETTING", "BLOCK_NONE")
	CohereSafetySetting = GetEnvOrDefaultString("COHERE_SAFETY_SETTING", "NONE")

	// Initialize rate limit variables
	GlobalApiRateLimitEnable = GetEnvOrDefaultBool("GLOBAL_API_RATE_LIMIT_ENABLE", true)
	GlobalApiRateLimitNum = GetEnvOrDefault("GLOBAL_API_RATE_LIMIT", 360)
	GlobalApiRateLimitDuration = int64(GetEnvOrDefault("GLOBAL_API_RATE_LIMIT_DURATION", 180))

	GlobalWebRateLimitEnable = GetEnvOrDefaultBool("GLOBAL_WEB_RATE_LIMIT_ENABLE", true)
	GlobalWebRateLimitNum = GetEnvOrDefault("GLOBAL_WEB_RATE_LIMIT", 120)
	GlobalWebRateLimitDuration = int64(GetEnvOrDefault("GLOBAL_WEB_RATE_LIMIT_DURATION", 180))

	CriticalRateLimitEnable = GetEnvOrDefaultBool("CRITICAL_RATE_LIMIT_ENABLE", true)
	CriticalRateLimitNum = GetEnvOrDefault("CRITICAL_RATE_LIMIT", 20)
	CriticalRateLimitDuration = int64(GetEnvOrDefault("CRITICAL_RATE_LIMIT_DURATION", 20*60))

	SearchRateLimitEnable = GetEnvOrDefaultBool("SEARCH_RATE_LIMIT_ENABLE", true)
	SearchRateLimitNum = GetEnvOrDefault("SEARCH_RATE_LIMIT", 10)
	SearchRateLimitDuration = int64(GetEnvOrDefault("SEARCH_RATE_LIMIT_DURATION", 60))
	initConstantEnv()
}

func initUserSessionSettings() {
	UserSessionActiveLimit = positiveUserSessionEnv("USER_SESSION_ACTIVE_LIMIT", DefaultUserSessionActiveLimit)
	UserSessionIssuanceLimit = positiveUserSessionEnv("USER_SESSION_ISSUANCE_LIMIT", DefaultUserSessionIssuanceLimit)
	UserSessionIssuanceWindowSeconds = int64(positiveUserSessionEnv("USER_SESSION_ISSUANCE_WINDOW_SECONDS", DefaultUserSessionIssuanceWindowSeconds))
	UserSessionRevokedRetentionDays = positiveUserSessionEnv("USER_SESSION_REVOKED_RETENTION_DAYS", DefaultUserSessionRevokedRetentionDays)
	UserSessionHourlyAlertThreshold = positiveUserSessionEnv("USER_SESSION_HOURLY_ALERT_THRESHOLD", DefaultUserSessionHourlyAlertThreshold)

	const secondsPerDay = 24 * 60 * 60
	if int64(UserSessionRevokedRetentionDays) > math.MaxInt64/secondsPerDay {
		SysError(fmt.Sprintf(
			"USER_SESSION_REVOKED_RETENTION_DAYS is too large, using default value: %d",
			DefaultUserSessionRevokedRetentionDays,
		))
		UserSessionRevokedRetentionDays = DefaultUserSessionRevokedRetentionDays
	}
	retentionSeconds := int64(UserSessionRevokedRetentionDays) * secondsPerDay
	if UserSessionIssuanceWindowSeconds > retentionSeconds {
		configuredWindow := UserSessionIssuanceWindowSeconds
		UserSessionIssuanceWindowSeconds = retentionSeconds
		SysError(fmt.Sprintf(
			"USER_SESSION_ISSUANCE_WINDOW_SECONDS exceeds revoked retention; configured_window_seconds=%d revoked_retention_seconds=%d effective_window_seconds=%d",
			configuredWindow,
			retentionSeconds,
			UserSessionIssuanceWindowSeconds,
		))
	}
}

func positiveUserSessionEnv(name string, fallback int) int {
	value := GetEnvOrDefault(name, fallback)
	if value <= 0 {
		SysError(fmt.Sprintf("%s must be positive, using default value: %d", name, fallback))
		return fallback
	}
	return value
}

func initConstantEnv() {
	constant.StreamingTimeout = GetEnvOrDefault("STREAMING_TIMEOUT", 300)
	constant.DifyDebug = GetEnvOrDefaultBool("DIFY_DEBUG", true)
	constant.MaxFileDownloadMB = GetEnvOrDefault("MAX_FILE_DOWNLOAD_MB", 64)
	constant.StreamScannerMaxBufferMB = GetEnvOrDefault("STREAM_SCANNER_MAX_BUFFER_MB", 128)
	// MaxRequestBodyMB 请求体最大大小（解压后），用于防止超大请求/zip bomb导致内存暴涨
	constant.MaxRequestBodyMB = GetEnvOrDefault("MAX_REQUEST_BODY_MB", 128)
	constant.AnonymousRequestBodyLimitKB = GetEnvOrDefault("ANONYMOUS_REQUEST_BODY_LIMIT_KB", 512)
	// ForceStreamOption 覆盖请求参数，强制返回usage信息
	constant.ForceStreamOption = GetEnvOrDefaultBool("FORCE_STREAM_OPTION", true)
	constant.CountToken = GetEnvOrDefaultBool("CountToken", true)
	constant.GetMediaToken = GetEnvOrDefaultBool("GET_MEDIA_TOKEN", true)
	constant.GetMediaTokenNotStream = GetEnvOrDefaultBool("GET_MEDIA_TOKEN_NOT_STREAM", false)
	constant.UpdateTask = GetEnvOrDefaultBool("UPDATE_TASK", true)
	constant.TaskPluginEnabled = GetEnvOrDefaultBool("TASK_PLUGIN_ENABLED", true)
	constant.AzureDefaultAPIVersion = GetEnvOrDefaultString("AZURE_DEFAULT_API_VERSION", "2025-04-01-preview")
	constant.NotifyLimitCount = GetEnvOrDefault("NOTIFY_LIMIT_COUNT", 2)
	constant.NotificationLimitDurationMinute = GetEnvOrDefault("NOTIFICATION_LIMIT_DURATION_MINUTE", 10)
	// GenerateDefaultToken 是否生成初始令牌，默认关闭。
	constant.GenerateDefaultToken = GetEnvOrDefaultBool("GENERATE_DEFAULT_TOKEN", false)
	// 是否启用错误日志
	constant.ErrorLogEnabled = GetEnvOrDefaultBool("ERROR_LOG_ENABLED", false)
	// 任务轮询时查询的最大数量
	constant.TaskQueryLimit = GetEnvOrDefault("TASK_QUERY_LIMIT", 1000)
	// 异步任务超时时间（分钟），超过此时间未完成的任务将被标记为失败并退款。0 表示禁用。
	constant.TaskTimeoutMinutes = GetEnvOrDefault("TASK_TIMEOUT_MINUTES", 1440)
	// Consecutive unrecognized/transient poll failures before the task is failed and refunded.
	constant.TaskPollMaxFailures = GetEnvOrDefault("TASK_POLL_MAX_FAILURES", 20)
	// 声明式任务协议桥只观察数据库；这些值控制一次客户端观察连接，
	// 不改变后台轮询或结算生命周期。
	constant.TaskPluginProtocolTimeoutSeconds = GetEnvOrDefault("TASK_PLUGIN_PROTOCOL_TIMEOUT_SECONDS", 600)
	constant.TaskPluginProtocolTickMilliseconds = GetEnvOrDefault("TASK_PLUGIN_PROTOCOL_TICK_MILLISECONDS", 2000)
	constant.TaskPluginProtocolTickJitterMilliseconds = GetEnvOrDefault("TASK_PLUGIN_PROTOCOL_TICK_JITTER_MILLISECONDS", 500)
	constant.TaskPluginProtocolHeartbeatSeconds = GetEnvOrDefault("TASK_PLUGIN_PROTOCOL_HEARTBEAT_SECONDS", 15)

	soraPatchStr := GetEnvOrDefaultString("TASK_PRICE_PATCH", "")
	if soraPatchStr != "" {
		var taskPricePatches []string
		soraPatches := strings.SplitSeq(soraPatchStr, ",")
		for patch := range soraPatches {
			trimmedPatch := strings.TrimSpace(patch)
			if trimmedPatch != "" {
				taskPricePatches = append(taskPricePatches, trimmedPatch)
			}
		}
		constant.TaskPricePatches = taskPricePatches
	}

	// Initialize trusted redirect domains for URL validation
	trustedDomainsStr := GetEnvOrDefaultString("TRUSTED_REDIRECT_DOMAINS", "")
	var trustedDomains []string
	domains := strings.SplitSeq(trustedDomainsStr, ",")
	for domain := range domains {
		trimmedDomain := strings.TrimSpace(domain)
		if trimmedDomain != "" {
			// Normalize domain to lowercase
			trustedDomains = append(trustedDomains, strings.ToLower(trimmedDomain))
		}
	}
	constant.TrustedRedirectDomains = trustedDomains
}
