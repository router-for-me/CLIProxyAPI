package credential_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/credential"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/quota"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// 兑换码模板服务测试
// ============================================================

// ------------------------------------------------------------
// Mock — 轻量级模板存储 Mock
// 仅用于单元测试，不依赖真实数据库
// ------------------------------------------------------------

type mockTemplateStore struct {
	templates map[int64]*db.RedemptionTemplate
	claims    map[string]int // key: "userID:templateID"
	nextID    int64
}

func newMockTemplateStore() *mockTemplateStore {
	return &mockTemplateStore{
		templates: make(map[int64]*db.RedemptionTemplate),
		claims:    make(map[string]int),
		nextID:    1,
	}
}

func (m *mockTemplateStore) CreateTemplate(_ context.Context, tpl *db.RedemptionTemplate) error {
	tpl.ID = m.nextID
	m.nextID++
	m.templates[tpl.ID] = tpl
	return nil
}

func (m *mockTemplateStore) GetTemplateByID(_ context.Context, id int64) (*db.RedemptionTemplate, error) {
	tpl, ok := m.templates[id]
	if !ok {
		return nil, fmt.Errorf("模板 id=%d 不存在", id)
	}
	return tpl, nil
}

func (m *mockTemplateStore) ListTemplates(_ context.Context) ([]*db.RedemptionTemplate, error) {
	result := make([]*db.RedemptionTemplate, 0, len(m.templates))
	for _, tpl := range m.templates {
		result = append(result, tpl)
	}
	return result, nil
}

func (m *mockTemplateStore) IncrementTemplateIssuedCount(_ context.Context, id int64) error {
	tpl, ok := m.templates[id]
	if !ok {
		return fmt.Errorf("模板 id=%d 不存在", id)
	}
	tpl.IssuedCount++
	return nil
}

func (m *mockTemplateStore) CountTemplateClaimsByUser(_ context.Context, userID, templateID int64) (int, error) {
	key := fmt.Sprintf("%d:%d", userID, templateID)
	return m.claims[key], nil
}

// incrementClaim 手动递增领取计数（模拟 ClaimTemplate 的副作用）
func (m *mockTemplateStore) incrementClaim(userID, templateID int64) {
	key := fmt.Sprintf("%d:%d", userID, templateID)
	m.claims[key]++
}

// ------------------------------------------------------------
// 测试辅助
// ------------------------------------------------------------

func newTestTemplateService(t *testing.T) (*credential.TemplateService, *mockTemplateStore, db.Store) {
	t.Helper()
	ctx := context.Background()

	store, err := db.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("创建存储失败: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	mockTpl := newMockTemplateStore()
	engine := quota.NewEngine(store)
	svc := credential.NewTemplateService(mockTpl, store, engine)
	return svc, mockTpl, store
}

// ============================================================
// 模板创建测试
// ============================================================

func TestCreateTemplate_Success(t *testing.T) {
	svc, _, _ := newTestTemplateService(t)
	ctx := context.Background()

	err := svc.CreateTemplate(ctx, "新手礼包", "注册用户额度奖励", db.QuotaGrant{
		ModelPattern: "claude-*",
		Requests:     50,
		QuotaType:    db.QuotaCount,
	}, 1, 1000)
	if err != nil {
		t.Fatalf("创建模板失败: %v", err)
	}
}

func TestCreateTemplate_EmptyName(t *testing.T) {
	svc, _, _ := newTestTemplateService(t)
	ctx := context.Background()

	err := svc.CreateTemplate(ctx, "", "描述", db.QuotaGrant{
		ModelPattern: "*",
		Requests:     10,
		QuotaType:    db.QuotaCount,
	}, 1, 100)
	if err == nil {
		t.Fatal("空名称应返回错误")
	}
}

func TestCreateTemplate_InvalidLimits(t *testing.T) {
	svc, _, _ := newTestTemplateService(t)
	ctx := context.Background()

	// maxPerUser = 0
	err := svc.CreateTemplate(ctx, "坏模板", "描述", db.QuotaGrant{
		ModelPattern: "*",
		Requests:     10,
		QuotaType:    db.QuotaCount,
	}, 0, 100)
	if err == nil {
		t.Fatal("maxPerUser=0 应返回错误")
	}

	// totalLimit = 0
	err = svc.CreateTemplate(ctx, "坏模板", "描述", db.QuotaGrant{
		ModelPattern: "*",
		Requests:     10,
		QuotaType:    db.QuotaCount,
	}, 1, 0)
	if err == nil {
		t.Fatal("totalLimit=0 应返回错误")
	}
}

// ============================================================
// 列表测试
// ============================================================

func TestListTemplates(t *testing.T) {
	svc, _, _ := newTestTemplateService(t)
	ctx := context.Background()

	// 创建两个模板
	svc.CreateTemplate(ctx, "模板A", "A", db.QuotaGrant{
		ModelPattern: "*", Requests: 10, QuotaType: db.QuotaCount,
	}, 1, 100)
	svc.CreateTemplate(ctx, "模板B", "B", db.QuotaGrant{
		ModelPattern: "*", Requests: 20, QuotaType: db.QuotaCount,
	}, 2, 200)

	templates, err := svc.ListTemplates(ctx)
	if err != nil {
		t.Fatalf("列出模板失败: %v", err)
	}
	if len(templates) != 2 {
		t.Fatalf("期望 2 个模板，实际: %d", len(templates))
	}
}

// ============================================================
// 领取测试
// ============================================================

func TestClaimTemplate_Success(t *testing.T) {
	svc, _, store := newTestTemplateService(t)
	ctx := context.Background()

	userID := seedUser(t, store, "claimer", "claimer@example.com")

	svc.CreateTemplate(ctx, "领取测试", "描述", db.QuotaGrant{
		ModelPattern: "gpt-*",
		Requests:     30,
		QuotaType:    db.QuotaCount,
	}, 3, 100)

	code, err := svc.ClaimTemplate(ctx, userID, 1)
	if err != nil {
		t.Fatalf("领取失败: %v", err)
	}
	if code == "" {
		t.Fatal("返回的码不应为空")
	}
}

func TestClaimTemplate_ExceedUserLimit(t *testing.T) {
	svc, mockTpl, store := newTestTemplateService(t)
	ctx := context.Background()

	userID := seedUser(t, store, "greedy", "greedy@example.com")

	svc.CreateTemplate(ctx, "限量模板", "描述", db.QuotaGrant{
		ModelPattern: "*",
		Requests:     5,
		QuotaType:    db.QuotaCount,
	}, 1, 100) // 每人限领 1 次

	// 第一次领取
	_, err := svc.ClaimTemplate(ctx, userID, 1)
	if err != nil {
		t.Fatalf("首次领取失败: %v", err)
	}

	// 手动递增 mock 的领取计数（模拟 DB 记录）
	mockTpl.incrementClaim(userID, 1)

	// 第二次领取应失败
	_, err = svc.ClaimTemplate(ctx, userID, 1)
	if err == nil {
		t.Fatal("超出用户领取上限应返回错误")
	}
}

func TestClaimTemplate_TotalLimitReached(t *testing.T) {
	svc, mockTpl, store := newTestTemplateService(t)
	ctx := context.Background()

	user1 := seedUser(t, store, "first", "first@example.com")
	user2 := seedUser(t, store, "second", "second@example.com")

	svc.CreateTemplate(ctx, "限量一个", "描述", db.QuotaGrant{
		ModelPattern: "*",
		Requests:     5,
		QuotaType:    db.QuotaCount,
	}, 1, 1) // 总共只发 1 个

	// 第一人领取成功
	_, err := svc.ClaimTemplate(ctx, user1, 1)
	if err != nil {
		t.Fatalf("首人领取失败: %v", err)
	}
	mockTpl.incrementClaim(user1, 1)

	// 第二人领取应失败（总量耗尽）
	_, err = svc.ClaimTemplate(ctx, user2, 1)
	if err == nil {
		t.Fatal("总量耗尽时应返回错误")
	}
}
