package service

import (
	"encoding/base64"
	"fmt"
	"strings"
)

func parseAudio(audioBase64 string, format string) (duration float64, err error) {
	audioData, err := base64.StdEncoding.DecodeString(audioBase64)
	if err != nil {
		return 0, fmt.Errorf("base64 decode error: %v", err)
	}

	var samplesCount int
	var sampleRate int

	switch format {
	case "pcm16":
		samplesCount = len(audioData) / 2 // 16位 = 2字节每样本
		sampleRate = 24000                // 24kHz
	case "g711_ulaw", "g711_alaw":
		samplesCount = len(audioData) // 8位 = 1字节每样本
		sampleRate = 8000             // 8kHz
	default:
		samplesCount = len(audioData) // 8位 = 1字节每样本
		sampleRate = 8000             // 8kHz
	}

	duration = float64(samplesCount) / float64(sampleRate)
	return duration, nil
}

func DecodeBase64AudioData(audioBase64 string) (string, error) {
	// 检查并移除 data:audio/xxx;base64, 前缀
	idx := strings.Index(audioBase64, ",")
	if idx != -1 {
		audioBase64 = audioBase64[idx+1:]
	}

	// 解码 Base64 数据
	_, err := base64.StdEncoding.DecodeString(audioBase64)
	if err != nil {
		return "", fmt.Errorf("base64 decode error: %v", err)
	}

	return audioBase64, nil
}
