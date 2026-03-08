package user_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/user"
)

func TestEmailService_VerifyCode(t *testing.T) {
	svc := user.NewEmailService("", 0, "", "", "", false)

	// 测试无验证码时返回 false
	if svc.VerifyCode("test@example.com", "123456") {
		t.Fatal("无验证码时应返回 false")
	}
}
