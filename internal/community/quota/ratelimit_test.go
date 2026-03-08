package quota_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/quota"
)

func TestRPMLimiter_Allow(t *testing.T) {
	limiter := quota.NewRPMLimiter(3) // 每分钟 3 次

	if !limiter.Allow(1) {
		t.Fatal("第 1 次请求应该允许")
	}
	if !limiter.Allow(1) {
		t.Fatal("第 2 次请求应该允许")
	}
	if !limiter.Allow(1) {
		t.Fatal("第 3 次请求应该允许")
	}
	if limiter.Allow(1) {
		t.Fatal("第 4 次请求应该拒绝")
	}
}

func TestRPMLimiter_DifferentUsers(t *testing.T) {
	limiter := quota.NewRPMLimiter(1)

	if !limiter.Allow(1) {
		t.Fatal("用户 1 应该允许")
	}
	if !limiter.Allow(2) {
		t.Fatal("用户 2 应该允许（独立计数）")
	}
	if limiter.Allow(1) {
		t.Fatal("用户 1 第二次应该拒绝")
	}
}

func TestRPMLimiter_Count(t *testing.T) {
	limiter := quota.NewRPMLimiter(10)

	limiter.Allow(1)
	limiter.Allow(1)
	limiter.Allow(1)

	if count := limiter.Count(1); count != 3 {
		t.Fatalf("期望 3, got %d", count)
	}
	if count := limiter.Count(2); count != 0 {
		t.Fatalf("期望 0, got %d", count)
	}
}
