package user

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"math/big"
	"net/smtp"
	"sync"
	"time"
)

// ============================================================
// 邮箱验证码服务 — SMTP 发送 + 内存缓存验证码
// ============================================================

// EmailService 邮件验证服务
type EmailService struct {
	host     string
	port     int
	username string
	password string
	from     string
	useTLS   bool

	mu    sync.RWMutex
	codes map[string]*verifyCode // email -> code
}

type verifyCode struct {
	Code      string
	ExpiresAt time.Time
}

// NewEmailService 创建邮件服务
func NewEmailService(host string, port int, username, password, from string, useTLS bool) *EmailService {
	return &EmailService{
		host:     host,
		port:     port,
		username: username,
		password: password,
		from:     from,
		useTLS:   useTLS,
		codes:    make(map[string]*verifyCode),
	}
}

// SendVerificationCode 发送验证码
// 同一邮箱 60 秒内不可重复发送
func (s *EmailService) SendVerificationCode(email string) error {
	// 防刷：使用写锁原子检查+写入，防止并发绕过 60s 限制
	s.mu.Lock()
	existing, hasExisting := s.codes[email]
	if hasExisting {
		// 验证码 10 分钟有效，只有在距创建时间 < 60s 时才拒绝
		elapsed := time.Since(existing.ExpiresAt.Add(-10 * time.Minute))
		if elapsed < 60*time.Second {
			s.mu.Unlock()
			return fmt.Errorf("发送过于频繁，请 %d 秒后重试", 60-int(elapsed.Seconds()))
		}
	}

	code, err := generateNumericCode(6)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("生成验证码失败: %w", err)
	}

	// 缓存验证码（10分钟有效）
	s.codes[email] = &verifyCode{
		Code:      code,
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	s.mu.Unlock()

	// 发送邮件
	subject := "CLIProxyAPI 公益站 — 邮箱验证码"
	body := fmt.Sprintf("您的验证码是: %s\n\n此验证码 10 分钟内有效，请勿泄露给他人。", code)
	return s.sendMail(email, subject, body)
}

// VerifyCode 验证验证码
func (s *EmailService) VerifyCode(email, code string) bool {
	s.mu.RLock()
	vc, ok := s.codes[email]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(vc.ExpiresAt) {
		s.mu.Lock()
		delete(s.codes, email)
		s.mu.Unlock()
		return false
	}
	if subtle.ConstantTimeCompare([]byte(vc.Code), []byte(code)) != 1 {
		return false
	}
	// 验证成功后删除
	s.mu.Lock()
	delete(s.codes, email)
	s.mu.Unlock()
	return true
}

// CleanExpired 清理过期验证码
func (s *EmailService) CleanExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for email, vc := range s.codes {
		if now.After(vc.ExpiresAt) {
			delete(s.codes, email)
		}
	}
}

func (s *EmailService) sendMail(to, subject, body string) error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	auth := smtp.PlainAuth("", s.username, s.password, s.host)
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		s.from, to, subject, body)
	return smtp.SendMail(addr, auth, s.from, []string{to}, []byte(msg))
}

func generateNumericCode(n int) (string, error) {
	code := ""
	for i := 0; i < n; i++ {
		num, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		code += fmt.Sprintf("%d", num.Int64())
	}
	return code, nil
}
