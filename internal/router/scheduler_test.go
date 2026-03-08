package router_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/quota"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/router"
)

// ============================================================
//  Mock 实现 — 用于隔离测试调度器核心逻辑
// ============================================================

// mockCredentialStore 凭证存储的内存 mock
type mockCredentialStore struct {
	db.CredentialStore
	publicCreds map[string][]*db.Credential // provider -> credentials
}

func newMockCredentialStore() *mockCredentialStore {
	return &mockCredentialStore{
		publicCreds: make(map[string][]*db.Credential),
	}
}

func (m *mockCredentialStore) GetPublicPoolCredentials(_ context.Context, provider string) ([]*db.Credential, error) {
	creds, ok := m.publicCreds[provider]
	if !ok {
		return nil, nil
	}
	return creds, nil
}

func (m *mockCredentialStore) GetUserCredentials(_ context.Context, _ int64, _ string) ([]*db.Credential, error) {
	return nil, nil
}

func (m *mockCredentialStore) GetCredentialByID(_ context.Context, id string) (*db.Credential, error) {
	for _, creds := range m.publicCreds {
		for _, c := range creds {
			if c.ID == id {
				return c, nil
			}
		}
	}
	return nil, fmt.Errorf("凭证不存在: %s", id)
}

// mockUserStore 用户存储的内存 mock
type mockUserStore struct {
	db.UserStore
	users map[int64]*db.User
}

func newMockUserStore() *mockUserStore {
	return &mockUserStore{
		users: make(map[int64]*db.User),
	}
}

func (m *mockUserStore) GetUserByID(_ context.Context, id int64) (*db.User, error) {
	u, ok := m.users[id]
	if !ok {
		return nil, fmt.Errorf("用户不存在: %d", id)
	}
	return u, nil
}

// ============================================================
//  辅助函数
// ============================================================

// setupTestScheduler 创建测试用调度器，预装指定数量的凭证
func setupTestScheduler(strategy router.Strategy, credCount int) (*router.Scheduler, *mockCredentialStore) {
	credStore := newMockCredentialStore()
	userStore := newMockUserStore()

	// 创建公共池用户
	userStore.users[1] = &db.User{
		ID:       1,
		PoolMode: db.PoolPublic,
	}

	// 创建测试凭证
	creds := make([]*db.Credential, credCount)
	for i := 0; i < credCount; i++ {
		creds[i] = &db.Credential{
			ID:       fmt.Sprintf("cred-%d", i),
			Provider: "openai",
			Weight:   1,
			Enabled:  true,
			Health:   db.HealthHealthy,
		}
	}
	credStore.publicCreds["openai"] = creds

	poolMgr := quota.NewPoolManager(credStore, userStore)
	scheduler := router.NewScheduler(strategy, poolMgr, credStore)
	return scheduler, credStore
}

// ============================================================
//  测试: Select 基本流程
// ============================================================

func TestScheduler_Select_BasicRoundRobin(t *testing.T) {
	sched, _ := setupTestScheduler(router.NewWeightedRoundRobin(), 3)
	ctx := context.Background()

	// 连续选择应依次分配到不同凭证
	seen := make(map[string]int)
	for i := 0; i < 9; i++ {
		cred, err := sched.Select(ctx, 1, "openai")
		if err != nil {
			t.Fatalf("Select 第 %d 次失败: %v", i, err)
		}
		seen[cred.ID]++
		// 模拟请求完成，释放活跃连接
		sched.ReportResult(cred.ID, true, 100)
	}

	// 3 个凭证各被选中 3 次（权重均为 1）
	for id, count := range seen {
		if count != 3 {
			t.Errorf("凭证 %s 被选中 %d 次，期望 3 次", id, count)
		}
	}
}

func TestScheduler_Select_LeastLoad(t *testing.T) {
	sched, _ := setupTestScheduler(router.NewLeastLoad(), 3)
	ctx := context.Background()

	// 先选一次，不释放连接，使 cred-0 有 1 个活跃连接
	cred0, err := sched.Select(ctx, 1, "openai")
	if err != nil {
		t.Fatalf("首次 Select 失败: %v", err)
	}

	// 第二次应选择活跃连接为 0 的凭证
	cred1, err := sched.Select(ctx, 1, "openai")
	if err != nil {
		t.Fatalf("第二次 Select 失败: %v", err)
	}

	if cred0.ID == cred1.ID {
		t.Errorf("LeastLoad 应选择不同凭证，但两次都选了 %s", cred0.ID)
	}

	// 释放连接
	sched.ReportResult(cred0.ID, true, 50)
	sched.ReportResult(cred1.ID, true, 50)
}

func TestScheduler_Select_FillFirst(t *testing.T) {
	sched, _ := setupTestScheduler(router.NewFillFirst(), 3)
	ctx := context.Background()

	// FillFirst 应始终选择第一个凭证
	for i := 0; i < 5; i++ {
		cred, err := sched.Select(ctx, 1, "openai")
		if err != nil {
			t.Fatalf("Select 第 %d 次失败: %v", i, err)
		}
		if cred.ID != "cred-0" {
			t.Errorf("FillFirst 应选择 cred-0，实际选择了 %s", cred.ID)
		}
		sched.ReportResult(cred.ID, true, 100)
	}
}

func TestScheduler_Select_NoCreds(t *testing.T) {
	sched, _ := setupTestScheduler(router.NewLeastLoad(), 0)
	ctx := context.Background()

	_, err := sched.Select(ctx, 1, "openai")
	if err == nil {
		t.Error("无凭证时 Select 应返回错误")
	}
}

// ============================================================
//  测试: ReportResult 与熔断
// ============================================================

func TestScheduler_ReportResult_CircuitBreaker(t *testing.T) {
	sched, _ := setupTestScheduler(router.NewFillFirst(), 2)
	ctx := context.Background()

	// 对 cred-0 连续报告 10 次失败（超过 50% 错误率阈值）
	for i := 0; i < 10; i++ {
		cred, err := sched.Select(ctx, 1, "openai")
		if err != nil {
			t.Fatalf("Select 第 %d 次失败: %v", i, err)
		}
		if cred.ID != "cred-0" {
			// 一旦 cred-0 被熔断，后续应选择 cred-1
			break
		}
		sched.ReportResult(cred.ID, false, 500)
	}

	// 此时 cred-0 应已熔断，FillFirst 应选择 cred-1
	cred, err := sched.Select(ctx, 1, "openai")
	if err != nil {
		t.Fatalf("熔断后 Select 失败: %v", err)
	}
	if cred.ID != "cred-1" {
		t.Errorf("cred-0 熔断后应选择 cred-1，实际选择了 %s", cred.ID)
	}
	sched.ReportResult(cred.ID, true, 50)
}

func TestScheduler_ReportResult_SuccessNoCircuit(t *testing.T) {
	sched, _ := setupTestScheduler(router.NewFillFirst(), 1)
	ctx := context.Background()

	// 全部成功请求不应触发熔断
	for i := 0; i < 20; i++ {
		cred, err := sched.Select(ctx, 1, "openai")
		if err != nil {
			t.Fatalf("Select 第 %d 次失败: %v", i, err)
		}
		sched.ReportResult(cred.ID, true, 50)
	}

	// 仍可正常选择
	cred, err := sched.Select(ctx, 1, "openai")
	if err != nil {
		t.Fatalf("成功场景 Select 不应失败: %v", err)
	}
	if cred.ID != "cred-0" {
		t.Errorf("期望选择 cred-0，实际为 %s", cred.ID)
	}
	sched.ReportResult(cred.ID, true, 50)
}

// ============================================================
//  测试: SetStrategy 热切换
// ============================================================

func TestScheduler_SetStrategy(t *testing.T) {
	sched, _ := setupTestScheduler(router.NewFillFirst(), 3)
	ctx := context.Background()

	// 初始 FillFirst 始终选 cred-0
	cred, err := sched.Select(ctx, 1, "openai")
	if err != nil {
		t.Fatalf("Select 失败: %v", err)
	}
	if cred.ID != "cred-0" {
		t.Errorf("FillFirst 应选择 cred-0，实际为 %s", cred.ID)
	}
	sched.ReportResult(cred.ID, true, 50)

	// 切换到 LeastLoad
	sched.SetStrategy(router.NewLeastLoad())

	// 先让 cred-0 持有连接
	cred0, _ := sched.Select(ctx, 1, "openai")
	// 再选一次，LeastLoad 应选择不同的
	cred1, _ := sched.Select(ctx, 1, "openai")

	if cred0.ID == cred1.ID {
		t.Errorf("切换到 LeastLoad 后，两次选择不应相同: %s", cred0.ID)
	}

	sched.ReportResult(cred0.ID, true, 50)
	sched.ReportResult(cred1.ID, true, 50)
}
