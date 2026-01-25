package kiro

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

// Fingerprint 多维度指纹信息
type Fingerprint struct {
	SDKVersion          string // 1.0.20-1.0.27
	OSType              string // darwin/windows/linux
	OSVersion           string // 10.0.22621
	NodeVersion         string // 18.x/20.x/22.x
	KiroVersion         string // 0.3.x-0.8.x
	KiroHash            string // SHA256
	AcceptLanguage      string
	ScreenResolution    string // 1920x1080
	ColorDepth          int    // 24
	HardwareConcurrency int    // CPU 核心数
	TimezoneOffset      int
}

// FingerprintManager 指纹管理器
type FingerprintManager struct {
	mu           sync.RWMutex
	fingerprints map[string]*Fingerprint // tokenKey -> fingerprint
	rng          *rand.Rand
}

var (
	sdkVersions = []string{
		"1.0.20", "1.0.21", "1.0.22", "1.0.23",
		"1.0.24", "1.0.25", "1.0.26", "1.0.27",
	}
	osTypes = []string{"darwin", "windows", "linux"}
	osVersions = map[string][]string{
		"darwin":  {"14.0", "14.1", "14.2", "14.3", "14.4", "14.5", "15.0", "15.1"},
		"windows": {"10.0.19041", "10.0.19042", "10.0.19043", "10.0.19044", "10.0.22621", "10.0.22631"},
		"linux":   {"5.15.0", "6.1.0", "6.2.0", "6.5.0", "6.6.0", "6.8.0"},
	}
	nodeVersions = []string{
		"18.17.0", "18.18.0", "18.19.0", "18.20.0",
		"20.9.0", "20.10.0", "20.11.0", "20.12.0", "20.13.0",
		"22.0.0", "22.1.0", "22.2.0", "22.3.0",
	}
	kiroVersions = []string{
		"0.3.0", "0.3.1", "0.4.0", "0.4.1", "0.5.0", "0.5.1",
		"0.6.0", "0.6.1", "0.7.0", "0.7.1", "0.8.0", "0.8.1",
	}
	acceptLanguages = []string{
		"en-US,en;q=0.9",
		"en-GB,en;q=0.9",
		"zh-CN,zh;q=0.9,en;q=0.8",
		"zh-TW,zh;q=0.9,en;q=0.8",
		"ja-JP,ja;q=0.9,en;q=0.8",
		"ko-KR,ko;q=0.9,en;q=0.8",
		"de-DE,de;q=0.9,en;q=0.8",
		"fr-FR,fr;q=0.9,en;q=0.8",
	}
	screenResolutions = []string{
		"1920x1080", "2560x1440", "3840x2160",
		"1366x768", "1440x900", "1680x1050",
		"2560x1600", "3440x1440",
	}
	colorDepths          = []int{24, 32}
	hardwareConcurrencies = []int{4, 6, 8, 10, 12, 16, 20, 24, 32}
	timezoneOffsets      = []int{-480, -420, -360, -300, -240, 0, 60, 120, 480, 540}
)

// NewFingerprintManager 创建指纹管理器
func NewFingerprintManager() *FingerprintManager {
	return &FingerprintManager{
		fingerprints: make(map[string]*Fingerprint),
		rng:          rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// GetFingerprint 获取或生成 Token 关联的指纹
func (fm *FingerprintManager) GetFingerprint(tokenKey string) *Fingerprint {
	fm.mu.RLock()
	if fp, exists := fm.fingerprints[tokenKey]; exists {
		fm.mu.RUnlock()
		return fp
	}
	fm.mu.RUnlock()

	fm.mu.Lock()
	defer fm.mu.Unlock()

	if fp, exists := fm.fingerprints[tokenKey]; exists {
		return fp
	}

	fp := fm.generateFingerprint(tokenKey)
	fm.fingerprints[tokenKey] = fp
	return fp
}

// generateFingerprint 生成新的指纹
func (fm *FingerprintManager) generateFingerprint(tokenKey string) *Fingerprint {
	osType := fm.randomChoice(osTypes)
	osVersion := fm.randomChoice(osVersions[osType])
	kiroVersion := fm.randomChoice(kiroVersions)

	fp := &Fingerprint{
		SDKVersion:          fm.randomChoice(sdkVersions),
		OSType:              osType,
		OSVersion:           osVersion,
		NodeVersion:         fm.randomChoice(nodeVersions),
		KiroVersion:         kiroVersion,
		AcceptLanguage:      fm.randomChoice(acceptLanguages),
		ScreenResolution:    fm.randomChoice(screenResolutions),
		ColorDepth:          fm.randomIntChoice(colorDepths),
		HardwareConcurrency: fm.randomIntChoice(hardwareConcurrencies),
		TimezoneOffset:      fm.randomIntChoice(timezoneOffsets),
	}

	fp.KiroHash = fm.generateKiroHash(tokenKey, kiroVersion, osType)
	return fp
}

// generateKiroHash 生成 Kiro Hash
func (fm *FingerprintManager) generateKiroHash(tokenKey, kiroVersion, osType string) string {
	data := fmt.Sprintf("%s:%s:%s:%d", tokenKey, kiroVersion, osType, time.Now().UnixNano())
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// randomChoice 随机选择字符串
func (fm *FingerprintManager) randomChoice(choices []string) string {
	return choices[fm.rng.Intn(len(choices))]
}

// randomIntChoice 随机选择整数
func (fm *FingerprintManager) randomIntChoice(choices []int) int {
	return choices[fm.rng.Intn(len(choices))]
}

// ApplyToRequest 将指纹信息应用到 HTTP 请求头
func (fp *Fingerprint) ApplyToRequest(req *http.Request) {
	req.Header.Set("X-Kiro-SDK-Version", fp.SDKVersion)
	req.Header.Set("X-Kiro-OS-Type", fp.OSType)
	req.Header.Set("X-Kiro-OS-Version", fp.OSVersion)
	req.Header.Set("X-Kiro-Node-Version", fp.NodeVersion)
	req.Header.Set("X-Kiro-Version", fp.KiroVersion)
	req.Header.Set("X-Kiro-Hash", fp.KiroHash)
	req.Header.Set("Accept-Language", fp.AcceptLanguage)
	req.Header.Set("X-Screen-Resolution", fp.ScreenResolution)
	req.Header.Set("X-Color-Depth", fmt.Sprintf("%d", fp.ColorDepth))
	req.Header.Set("X-Hardware-Concurrency", fmt.Sprintf("%d", fp.HardwareConcurrency))
	req.Header.Set("X-Timezone-Offset", fmt.Sprintf("%d", fp.TimezoneOffset))
}

// RemoveFingerprint 移除 Token 关联的指纹
func (fm *FingerprintManager) RemoveFingerprint(tokenKey string) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	delete(fm.fingerprints, tokenKey)
}

// Count 返回当前管理的指纹数量
func (fm *FingerprintManager) Count() int {
	fm.mu.RLock()
	defer fm.mu.RUnlock()
	return len(fm.fingerprints)
}

// BuildUserAgent 构建 User-Agent 字符串 (Kiro IDE 风格)
// 格式: aws-sdk-js/{SDKVersion} ua/2.1 os/{OSType}#{OSVersion} lang/js md/nodejs#{NodeVersion} api/codewhispererstreaming#{SDKVersion} m/E KiroIDE-{KiroVersion}-{KiroHash}
func (fp *Fingerprint) BuildUserAgent() string {
	return fmt.Sprintf(
		"aws-sdk-js/%s ua/2.1 os/%s#%s lang/js md/nodejs#%s api/codewhispererstreaming#%s m/E KiroIDE-%s-%s",
		fp.SDKVersion,
		fp.OSType,
		fp.OSVersion,
		fp.NodeVersion,
		fp.SDKVersion,
		fp.KiroVersion,
		fp.KiroHash,
	)
}

// BuildAmzUserAgent 构建 X-Amz-User-Agent 字符串
// 格式: aws-sdk-js/{SDKVersion} KiroIDE-{KiroVersion}-{KiroHash}
func (fp *Fingerprint) BuildAmzUserAgent() string {
	return fmt.Sprintf(
		"aws-sdk-js/%s KiroIDE-%s-%s",
		fp.SDKVersion,
		fp.KiroVersion,
		fp.KiroHash,
	)
}
