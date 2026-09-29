package common

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
)

func Sha256Raw(data []byte) []byte {
	h := sha256.New()
	h.Write(data)
	return h.Sum(nil)
}

func Sha1Raw(data []byte) []byte {
	h := sha1.New()
	h.Write(data)
	return h.Sum(nil)
}

func Sha1(data []byte) string {
	return hex.EncodeToString(Sha1Raw(data))
}

func HmacSha256Raw(message, key []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(message)
	return h.Sum(nil)
}

func HmacSha256(message, key string) string {
	return hex.EncodeToString(HmacSha256Raw([]byte(message), []byte(key)))
}
