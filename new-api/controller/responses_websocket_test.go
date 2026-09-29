package controller

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var responsesWSTestUserSequence atomic.Int64

func setupResponsesWSRequestTest(t *testing.T) (*model.User, *model.Token) {
	t.Helper()
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousType := common.MainDatabaseType()
	previousLogType := common.LogDatabaseType()
	previousRedis := common.RedisEnabled
	previousRDB := common.RDB
	previousMaster, previousMemory, previousSQLite := common.IsMasterNode, common.MemoryCacheEnabled, common.SQLitePath
	previousEnabled := setting.ModelRequestRateLimitEnabled
	previousDuration := setting.ModelRequestRateLimitDurationMinutes
	previousTotal := setting.ModelRequestRateLimitCount
	previousSuccess := setting.ModelRequestRateLimitSuccessCount
	setting.ModelRequestRateLimitMutex.Lock()
	previousGroups := setting.ModelRequestRateLimitGroup
	setting.ModelRequestRateLimitGroup = nil
	setting.ModelRequestRateLimitMutex.Unlock()
	t.Setenv("SQL_DSN", os.Getenv("TEST_RESPONSES_SQL_DSN"))
	t.Setenv("LOG_SQL_DSN", os.Getenv("TEST_RESPONSES_LOG_SQL_DSN"))
	common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled = false, false, false
	common.SQLitePath = filepath.Join(t.TempDir(), "responses.db")
	require.NoError(t, model.InitDB())
	db := model.DB
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, model.InitLogDB())
	if model.LOG_DB != db {
		logSQL, err := model.LOG_DB.DB()
		require.NoError(t, err)
		logSQL.SetMaxOpenConns(1)
		t.Cleanup(func() { require.NoError(t, logSQL.Close()) })
	}
	setting.ModelRequestRateLimitEnabled = false
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SetDatabaseTypes(previousType, previousLogType)
		common.RedisEnabled = previousRedis
		common.RDB = previousRDB
		common.IsMasterNode, common.MemoryCacheEnabled, common.SQLitePath = previousMaster, previousMemory, previousSQLite
		setting.ModelRequestRateLimitEnabled = previousEnabled
		setting.ModelRequestRateLimitDurationMinutes = previousDuration
		setting.ModelRequestRateLimitCount = previousTotal
		setting.ModelRequestRateLimitSuccessCount = previousSuccess
		setting.ModelRequestRateLimitMutex.Lock()
		setting.ModelRequestRateLimitGroup = previousGroups
		setting.ModelRequestRateLimitMutex.Unlock()
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}))
	// The shared in-memory limiter outlives each database fixture. Give every
	// user a separate quota bucket, including when the tests run with -count.
	user := &model.User{Id: 5062000 + int(responsesWSTestUserSequence.Add(1)), Username: "responses-ws-user", Status: common.UserStatusEnabled, Group: "default", Quota: 1000, AuthVersion: 1}
	require.NoError(t, db.Create(user).Error)
	token := &model.Token{UserId: user.Id, Key: "responseswstoken", Status: common.TokenStatusEnabled, ExpiredTime: -1, RemainQuota: 100}
	require.NoError(t, db.Create(token).Error)
	t.Cleanup(func() {
		require.NoError(t, db.Unscoped().Delete(token).Error)
		require.NoError(t, db.Unscoped().Delete(user).Error)
	})
	return user, token
}

func newResponsesWSTestRunner(t *testing.T, token *model.Token) (relay.ResponsesWSRequestRunner, *http.Request) {
	t.Helper()
	var runner relay.ResponsesWSRequestRunner
	engine := gin.New()
	require.NoError(t, engine.SetTrustedProxies([]string{"127.0.0.1"}))
	engine.GET("/v1/responses", middleware.TokenAuth(), func(c *gin.Context) {
		runner = newResponsesWSRequestRunner(c)
	})
	request := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Authorization", "Bearer sk-"+token.Key)
	request.Header.Set("X-Forwarded-For", "203.0.113.8")
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Key", "handshake-key")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Extensions", "permessage-deflate")
	request.Header.Set("Sec-WebSocket-Protocol", "responses")
	request.Header.Set("Content-Length", "99")
	request.Header.Set("Content-Encoding", "gzip")
	request.Header.Set("X-Application-Header", "preserved")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.NotNil(t, runner)
	return runner, request
}

func TestResponsesWSRequestRunnerRefreshesBillingContextAndCleansBody(t *testing.T) {
	user, token := setupResponsesWSRequestTest(t)
	allowedIP := "203.0.113.8"
	require.NoError(t, model.DB.Model(token).Update("allow_ips", allowedIP).Error)
	runner, handshake := newResponsesWSTestRunner(t, token)
	// A later upstream header mutation must not replace the client's credential.
	handshake.Header.Set("Authorization", "Bearer upstream-secret")
	handshake.Header.Set("X-Forwarded-For", "198.51.100.99")
	var storage common.BodyStorage
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"first"}`))
	require.Nil(t, runner(request, "ws-first", func(c *gin.Context) *types.NewAPIError {
		assert.Equal(t, user.Id, c.GetInt("id"))
		assert.Equal(t, 100, c.GetInt("token_quota"))
		assert.Equal(t, "203.0.113.8", c.ClientIP())
		assert.Equal(t, "ws-first", c.GetString(common.RequestIdKey))
		assert.Equal(t, "ws-first", c.Request.Context().Value(common.RequestIdKey))
		for _, header := range []string{"Connection", "Upgrade", "Sec-WebSocket-Key", "Sec-WebSocket-Version", "Sec-WebSocket-Extensions", "Sec-WebSocket-Protocol", "Content-Length", "Content-Encoding"} {
			assert.Empty(t, c.GetHeader(header))
		}
		assert.Equal(t, "preserved", c.GetHeader("X-Application-Header"))
		c.Set("previous_turn", true)
		var err error
		storage, err = common.GetBodyStorage(c)
		require.NoError(t, err)
		data, err := storage.Bytes()
		require.NoError(t, err)
		assert.JSONEq(t, `{"input":"first"}`, string(data))
		return nil
	}))
	_, err := storage.Bytes()
	assert.ErrorIs(t, err, common.ErrStorageClosed)
	require.NoError(t, model.DB.Model(token).Updates(map[string]any{
		"remain_quota": 2, "model_limits_enabled": true, "model_limits": "gpt-5.1",
	}).Error)
	require.NoError(t, model.DB.Model(user).Updates(map[string]any{
		"group": "updated", "setting": `{"billing_preference":"wallet_only"}`,
	}).Error)
	request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"input":"second"}`))
	require.Nil(t, runner(request, "ws-second", func(c *gin.Context) *types.NewAPIError {
		assert.Equal(t, 2, c.GetInt("token_quota"))
		assert.True(t, c.GetBool("token_model_limit_enabled"))
		modelLimits, _ := c.Get("token_model_limit")
		assert.Equal(t, map[string]bool{"gpt-5.1": true}, modelLimits)
		assert.Equal(t, "updated", common.GetContextKeyString(c, constant.ContextKeyUsingGroup))
		userSetting, ok := common.GetContextKeyType[dto.UserSetting](c, constant.ContextKeyUserSetting)
		require.True(t, ok)
		assert.Equal(t, "wallet_only", userSetting.BillingPreference)
		assert.Equal(t, "ws-second", c.GetString(common.RequestIdKey))
		_, leaked := c.Get("previous_turn")
		assert.False(t, leaked)
		_, leaked = c.Get(common.KeyBodyStorage)
		assert.False(t, leaked)
		return nil
	}))
}

func TestResponsesWSRequestRunnerRejectsRevokedCredentials(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		update func(*testing.T, *model.User, *model.Token)
	}{
		{name: "disabled token", status: http.StatusUnauthorized, update: func(t *testing.T, _ *model.User, token *model.Token) {
			require.NoError(t, model.DB.Model(token).Update("status", common.TokenStatusDisabled).Error)
		}},
		{name: "expired token", status: http.StatusUnauthorized, update: func(t *testing.T, _ *model.User, token *model.Token) {
			require.NoError(t, model.DB.Model(token).Update("expired_time", time.Now().Add(-time.Minute).Unix()).Error)
		}},
		{name: "exhausted token", status: http.StatusUnauthorized, update: func(t *testing.T, _ *model.User, token *model.Token) {
			require.NoError(t, model.DB.Model(token).Update("remain_quota", 0).Error)
		}},
		{name: "disabled user", status: http.StatusForbidden, update: func(t *testing.T, user *model.User, _ *model.Token) {
			require.NoError(t, model.DB.Model(user).Update("status", common.UserStatusDisabled).Error)
		}},
		{name: "deleted token", status: http.StatusUnauthorized, update: func(t *testing.T, _ *model.User, token *model.Token) {
			require.NoError(t, model.DB.Delete(token).Error)
		}},
		{name: "changed IP restriction", status: http.StatusForbidden, update: func(t *testing.T, _ *model.User, token *model.Token) {
			require.NoError(t, model.DB.Model(token).Update("allow_ips", "198.51.100.0/24").Error)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user, token := setupResponsesWSRequestTest(t)
			runner, _ := newResponsesWSTestRunner(t, token)
			require.Nil(t, runner(httptest.NewRequest(http.MethodPost, "/v1/responses", nil), "before", func(*gin.Context) *types.NewAPIError { return nil }))
			tc.update(t, user, token)
			called := false
			apiError := runner(httptest.NewRequest(http.MethodPost, "/v1/responses", nil), "after", func(*gin.Context) *types.NewAPIError {
				called = true
				return nil
			})
			require.NotNil(t, apiError)
			assert.Equal(t, tc.status, apiError.StatusCode)
			assert.False(t, called)
			assert.NotContains(t, apiError.Error(), token.Key)
		})
	}
}

func TestResponsesWSRequestRunnerUsesExistingMemoryRateLimit(t *testing.T) {
	_, token := setupResponsesWSRequestTest(t)
	setting.ModelRequestRateLimitEnabled = true
	setting.ModelRequestRateLimitDurationMinutes = 1
	setting.ModelRequestRateLimitCount = 1
	setting.ModelRequestRateLimitSuccessCount = 100
	runner, _ := newResponsesWSTestRunner(t, token)
	called := 0
	handle := func(*gin.Context) *types.NewAPIError { called++; return nil }
	require.Nil(t, runner(httptest.NewRequest(http.MethodPost, "/v1/responses", nil), "first", handle))
	apiError := runner(httptest.NewRequest(http.MethodPost, "/v1/responses", nil), "limited", handle)
	require.NotNil(t, apiError)
	assert.Equal(t, http.StatusTooManyRequests, apiError.StatusCode)
	assert.Equal(t, http.StatusText(http.StatusTooManyRequests), apiError.Error())
	assert.Equal(t, 1, called)
}

func TestResponsesWSRequestRunnerSharesRedisSuccessLimitWithHTTP(t *testing.T) {
	_, token := setupResponsesWSRequestTest(t)
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	common.RedisEnabled = true
	common.RDB = client
	setting.ModelRequestRateLimitEnabled = true
	setting.ModelRequestRateLimitDurationMinutes = 1
	setting.ModelRequestRateLimitCount = 0
	setting.ModelRequestRateLimitSuccessCount = 1
	runner, _ := newResponsesWSTestRunner(t, token)
	failed := types.NewErrorWithStatusCode(errors.New("invalid input"), types.ErrorCodeInvalidRequest, http.StatusBadRequest)
	assert.Same(t, failed, runner(httptest.NewRequest(http.MethodPost, "/v1/responses", nil), "failed", func(*gin.Context) *types.NewAPIError { return failed }))
	require.Nil(t, runner(httptest.NewRequest(http.MethodPost, "/v1/responses", nil), "successful", func(*gin.Context) *types.NewAPIError { return nil }))
	engine := gin.New()
	called := false
	engine.POST("/v1/responses", middleware.TokenAuth(), middleware.ModelRequestRateLimit(), func(c *gin.Context) { called = true })
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("Authorization", "Bearer sk-"+token.Key)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	assert.Equal(t, http.StatusTooManyRequests, response.Code)
	assert.False(t, called)
}

type responsesWSBillingTest struct {
	httpUpstream http.HandlerFunc
	httpDone     chan struct{}
	user         *model.User
	token        *model.Token
	channel      *model.Channel
	client       *websocket.Conn
	done         chan struct{}
	upstreamDone chan struct{}
	connections  atomic.Int32
	gatewayURL   string
}

func (fixture *responsesWSBillingTest) closeAndWait(t *testing.T) {
	t.Helper()
	_ = fixture.client.Close()
	select {
	case <-fixture.done:
	case <-time.After(3 * time.Second):
		t.Error("gateway handler did not stop after closing its client")
	}
	if fixture.connections.Load() > 0 {
		select {
		case <-fixture.upstreamDone:
		case <-time.After(3 * time.Second):
			t.Error("upstream connection was not closed")
		}
	}
	// Health classification is synchronous at the request boundary; only the
	// Redis write is asynchronous, see waitPerfCounters.
}

// waitPerfCounters waits for the asynchronous health samples of the fixture
// model to reach wantRequests and returns the request and success counters.
func waitPerfCounters(t *testing.T, wantRequests int64) (requests, successes int64) {
	t.Helper()
	require.Eventually(t, func() bool {
		requests, successes = 0, 0
		keys, err := common.RDB.Keys(context.Background(), "perf:ws-billing:*").Result()
		if err != nil {
			return false
		}
		for _, key := range keys {
			n, _ := common.RDB.HGet(context.Background(), key, "req").Int64()
			requests += n
			n, _ = common.RDB.HGet(context.Background(), key, "ok").Int64()
			successes += n
		}
		return requests >= wantRequests
	}, 3*time.Second, 10*time.Millisecond)
	return requests, successes
}

func newResponsesWSBillingTest(t *testing.T, expression string, handle func(*websocket.Conn, *http.Request), httpEvents ...string) *responsesWSBillingTest {
	t.Helper()
	user, token := setupResponsesWSRequestTest(t)
	previousTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = previousTimeout })
	previousBatch, previousLogs, previousCount, previousQuota := common.BatchUpdateEnabled, common.LogConsumeEnabled, constant.CountToken, common.QuotaPerUnit
	common.BatchUpdateEnabled, common.LogConsumeEnabled, constant.CountToken, common.QuotaPerUnit = false, true, false, 500000
	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		switch key {
		case "billing_setting.billing_mode", "billing_setting.billing_expr", "group_ratio_setting.group_ratio", "perf_metrics_setting.enabled":
			saved[key] = value
		}
		return nil
	}))
	t.Cleanup(func() {
		common.BatchUpdateEnabled, common.LogConsumeEnabled, constant.CountToken, common.QuotaPerUnit = previousBatch, previousLogs, previousCount, previousQuota
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})
	expressions, err := common.Marshal(map[string]string{"ws-billing": expression})
	require.NoError(t, err)
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":    `{"ws-billing":"tiered_expr"}`,
		"billing_setting.billing_expr":    string(expressions),
		"group_ratio_setting.group_ratio": `{"default":1}`,
		"perf_metrics_setting.enabled":    "true",
	}))
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Ability{}))
	require.NoError(t, model.LOG_DB.AutoMigrate(&model.Log{}))
	require.NoError(t, model.DB.Model(user).Updates(map[string]any{"quota": 100000, "setting": `{"billing_preference":"wallet_only"}`}).Error)
	require.NoError(t, model.DB.Model(token).Update("remain_quota", 3000).Error)

	fixture := &responsesWSBillingTest{user: user, token: token, done: make(chan struct{}), upstreamDone: make(chan struct{}), httpDone: make(chan struct{}, 1)}
	redisServer := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	common.RDB, common.RedisEnabled = redisClient, true
	t.Cleanup(func() { require.NoError(t, redisClient.Close()) })
	var upstreamClosed sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !websocket.IsWebSocketUpgrade(r) {
			if fixture.httpUpstream != nil {
				fixture.httpUpstream(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			for _, event := range httpEvents {
				_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
			}
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if !assert.NoError(t, err) {
			return
		}
		fixture.connections.Add(1)
		defer ws.Close()
		defer upstreamClosed.Do(func() { close(fixture.upstreamDone) })
		handle(ws, r)
	}))
	t.Cleanup(upstream.Close)
	channel := &model.Channel{Name: "responses-ws-upstream", Key: "upstream-first\nupstream-second", Status: common.ChannelStatusEnabled, Type: constant.ChannelTypeOpenAI, Group: "default", Models: "ws-billing", BaseURL: &upstream.URL,
		ChannelInfo: model.ChannelInfo{IsMultiKey: true, MultiKeySize: 2, MultiKeyMode: constant.MultiKeyModePolling}}
	modelMapping := `{"ws-billing":"gpt-4o"}`
	channel.ModelMapping = &modelMapping
	channel.SetSetting(dto.ChannelSettings{ResponsesWebSocketEnabled: true})
	channel.SetOtherSettings(dto.ChannelOtherSettings{AllowServiceTier: true})
	require.NoError(t, model.DB.Create(channel).Error)
	fixture.channel = channel
	require.NoError(t, model.DB.Create(&model.Ability{ChannelId: channel.Id, Model: "ws-billing", Group: "default", Enabled: true}).Error)
	t.Cleanup(func() {
		require.NoError(t, model.LOG_DB.Where("token_id = ?", token.Id).Delete(&model.Log{}).Error)
		require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Delete(&model.Ability{}).Error)
		require.NoError(t, model.DB.Delete(channel).Error)
	})
	engine := gin.New()
	engine.GET("/v1/responses", middleware.TokenAuth(), func(c *gin.Context) {
		defer close(fixture.done)
		c.Set(common.RequestIdKey, "responses-ws-billing")
		ResponsesWebSocket(c)
	})
	engine.POST("/v1/responses", middleware.TokenAuth(), middleware.ModelRequestRateLimit(), middleware.Distribute(), func(c *gin.Context) {
		defer func() { fixture.httpDone <- struct{}{} }()
		c.Set(common.RequestIdKey, "responses-http-billing")
		Relay(c, types.RelayFormatOpenAIResponses)
	})
	gateway := httptest.NewServer(engine)
	fixture.gatewayURL = gateway.URL
	t.Cleanup(gateway.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(gateway.URL, "http")+"/v1/responses", http.Header{"Authorization": []string{"Bearer sk-" + token.Key}})
	require.NoError(t, err)
	fixture.client = client
	t.Cleanup(func() { fixture.closeAndWait(t) })
	return fixture
}

func TestResponsesInterruptedStreamHealth(t *testing.T) {
	for _, scenario := range []string{"websocket timeout", "sse timeout", "sse client cancel"} {
		t.Run(scenario, func(t *testing.T) {
			events := []string{`{"type":"response.created","response":{"id":"partial","status":"in_progress"}}`, `{"type":"response.output_text.delta","delta":"hello"}`}
			fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(ws *websocket.Conn, _ *http.Request) {
				if _, _, err := ws.ReadMessage(); !assert.NoError(t, err) {
					return
				}
				for _, event := range events {
					if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(event))) {
						return
					}
				}
				_, _, _ = ws.ReadMessage()
			})
			constant.StreamingTimeout = 1
			fixture.httpUpstream = func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, event := range events {
					_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
				}
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}
			if scenario == "websocket timeout" {
				require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"ws-billing","input":"hello"}`)))
				assert.Equal(t, "response.created", readResponsesWSTestEvent(t, fixture.client)["type"])
				assert.Equal(t, "response.output_text.delta", readResponsesWSTestEvent(t, fixture.client)["type"])
				_, _, err := fixture.client.ReadMessage()
				require.Error(t, err, "idle timeout closes the incomplete stream")
			} else {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				request, err := http.NewRequestWithContext(ctx, http.MethodPost, fixture.gatewayURL+"/v1/responses", strings.NewReader(`{"model":"ws-billing","input":"hello","stream":true}`))
				require.NoError(t, err)
				request.Header.Set("Authorization", "Bearer sk-"+fixture.token.Key)
				request.Header.Set("Content-Type", "application/json")
				response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
				require.NoError(t, err)
				if scenario == "sse client cancel" {
					reader := bufio.NewReader(response.Body)
					for {
						line, err := reader.ReadString('\n')
						require.NoError(t, err)
						if strings.Contains(line, "response.output_text.delta") {
							break
						}
					}
					cancel()
				} else {
					_, err = io.Copy(io.Discard, response.Body)
					require.NoError(t, err)
				}
				require.NoError(t, response.Body.Close())
				select {
				case <-fixture.httpDone:
				case <-time.After(3 * time.Second):
					t.Fatal("interrupted SSE request did not finish")
				}
			}
			fixture.closeAndWait(t)
			assertResponsesWSAccounting(t, fixture, []int{1000})
			if scenario == "sse client cancel" {
				keys, err := common.RDB.Keys(context.Background(), "perf:ws-billing:*").Result()
				require.NoError(t, err)
				assert.Empty(t, keys, "client cancellation must not affect model health")
				return
			}
			requests, successes := waitPerfCounters(t, 1)
			assert.Equal(t, int64(1), requests)
			assert.Zero(t, successes)
		})
	}
}

func readResponsesWSTestEvent(t *testing.T, client *websocket.Conn) map[string]any {
	t.Helper()
	require.NoError(t, client.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, data, err := client.ReadMessage()
	require.NoError(t, err)
	var event map[string]any
	require.NoError(t, common.Unmarshal(data, &event))
	return event
}

func assertResponsesWSAccounting(t *testing.T, fixture *responsesWSBillingTest, expectedQuotas []int) {
	t.Helper()
	var logs []model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", model.LogTypeConsume).Order("id").Find(&logs).Error)
	require.Len(t, logs, len(expectedQuotas))
	var charged int
	for index, quota := range expectedQuotas {
		assert.Equal(t, quota, logs[index].Quota)
		charged += quota
	}
	require.NoError(t, model.DB.First(fixture.token, fixture.token.Id).Error)
	require.NoError(t, model.DB.First(fixture.user, fixture.user.Id).Error)
	assert.Equal(t, 3000-charged, fixture.token.RemainQuota)
	assert.Equal(t, charged, fixture.token.UsedQuota)
	assert.Equal(t, 100000-charged, fixture.user.Quota)
	assert.Equal(t, charged, fixture.user.UsedQuota)
}

func TestResponsesWebSocketReusesConnectionAndSettlesEachRequest(t *testing.T) {
	type upstreamRequest struct {
		Authorization      string
		Type               string `json:"type"`
		Model              string `json:"model"`
		PreviousResponseID string `json:"previous_response_id"`
		ServiceTier        string `json:"service_tier"`
	}
	received := make(chan upstreamRequest, 3)
	fixture := newResponsesWSBillingTest(t, `param("service_tier") == "priority" ? tier("priority", p * 4) : tier("base", p * 2)`, func(ws *websocket.Conn, r *http.Request) {
		for index := 1; ; index++ {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var event upstreamRequest
			if !assert.NoError(t, common.Unmarshal(data, &event)) {
				return
			}
			event.Authorization = r.Header.Get("Authorization")
			received <- event
			if index == 2 {
				duplicate := `{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":1000,"output_tokens":10,"total_tokens":1010}}}`
				if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(duplicate))) {
					return
				}
			}
			terminal := fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_%d","status":"completed","model":"ws-billing","output":[],"usage":{"input_tokens":1000,"output_tokens":10,"total_tokens":1010}}}`, index)
			if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(terminal))) {
				return
			}
		}
	})
	client, user, token := fixture.client, fixture.user, fixture.token

	for index, payload := range []string{
		`{"type":"response.create","model":"ws-billing","input":"first","service_tier":"default"}`,
		`{"type":"response.create","model":"ws-billing","input":"second","previous_response_id":"resp_1","service_tier":"priority"}`,
	} {
		require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte(payload)))
		require.NoError(t, client.SetReadDeadline(time.Now().Add(3*time.Second)))
		_, data, err := client.ReadMessage()
		require.NoError(t, err)
		var terminal struct {
			Type     string `json:"type"`
			Response struct {
				ID string `json:"id"`
			} `json:"response"`
		}
		require.NoError(t, common.Unmarshal(data, &terminal))
		require.Equal(t, "response.completed", terminal.Type, "unexpected response: %s", data)
		assert.Equal(t, fmt.Sprintf("resp_%d", index+1), terminal.Response.ID)
		observed := <-received
		assert.Equal(t, "Bearer upstream-first", observed.Authorization)
		assert.Equal(t, "gpt-4o", observed.Model)
		assert.Equal(t, "response.create", observed.Type)
		if index == 0 {
			assert.Empty(t, observed.PreviousResponseID)
			assert.Equal(t, "default", observed.ServiceTier)
		} else {
			assert.Equal(t, "resp_1", observed.PreviousResponseID)
			assert.Equal(t, "priority", observed.ServiceTier)
		}
	}

	require.NoError(t, client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"ws-billing","input":"exhausted"}`)))
	require.NoError(t, client.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, data, err := client.ReadMessage()
	require.NoError(t, err)
	var rejection struct {
		Type   string `json:"type"`
		Status int    `json:"status"`
	}
	require.NoError(t, common.Unmarshal(data, &rejection))
	assert.Equal(t, "error", rejection.Type)
	assert.Equal(t, http.StatusUnauthorized, rejection.Status)
	assert.Equal(t, int32(1), fixture.connections.Load())
	assert.Empty(t, received, "exhausted token must be rejected before contacting upstream")
	var logs []model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", model.LogTypeConsume).Order("id").Find(&logs).Error)
	require.Len(t, logs, 2)
	for index, expectedQuota := range []int{1000, 2000} {
		assert.Equal(t, expectedQuota, logs[index].Quota)
		assert.Equal(t, fmt.Sprintf("responses-ws-billing-ws-%d", index), logs[index].RequestId)
		assert.Equal(t, 1000, logs[index].PromptTokens)
		assert.Equal(t, 10, logs[index].CompletionTokens)
	}
	// Routing decisions are persisted per request: the first create selects a
	// channel, later creates on the same connection reuse it as a pin.
	for index, wantDecision := range []string{"attempt:channel_selected", "select:pinned_channel"} {
		var other struct {
			AdminInfo struct {
				RequestPolicy []struct {
					ChannelID int `json:"channel_id"`
					Decision  struct {
						Action string `json:"action"`
						Reason string `json:"reason"`
					} `json:"decision"`
				} `json:"request_policy"`
			} `json:"admin_info"`
		}
		require.NoError(t, common.UnmarshalJsonStr(logs[index].Other, &other))
		var decisions []string
		for _, event := range other.AdminInfo.RequestPolicy {
			assert.Equal(t, fixture.channel.Id, event.ChannelID)
			decisions = append(decisions, event.Decision.Action+":"+event.Decision.Reason)
		}
		assert.Contains(t, decisions, wantDecision, "request %d policy events: %v", index, decisions)
	}
	require.NoError(t, model.DB.First(token, token.Id).Error)
	require.NoError(t, model.DB.First(user, user.Id).Error)
	assert.Zero(t, token.RemainQuota)
	assert.Equal(t, 3000, token.UsedQuota)
	assert.Equal(t, 97000, user.Quota)
	assert.Equal(t, 3000, user.UsedQuota)
}

// Both transports must reach the same upstream target with the same
// credential placement and settle identically for every supported channel type.
func TestResponsesWebSocketDialsNativeResponsesChannelTypes(t *testing.T) {
	type upstreamTarget struct {
		Path, Authorization, QueryKey string
	}
	queryAuthRoute := dto.ChannelOtherSettings{AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{
		IncomingPath: "/v1/responses", UpstreamPath: "/upstream/responses",
		Auth: &dto.AdvancedCustomRouteAuth{Type: dto.AdvancedCustomAuthTypeQuery, Name: "api_key", Value: "{api_key}"},
	}}}}
	bearer := func(key string) upstreamTarget {
		return upstreamTarget{Path: "/v1/responses", Authorization: "Bearer " + key}
	}
	for _, tc := range []struct {
		name          string
		channelType   int
		otherSettings dto.ChannelOtherSettings
		want          func(key string) upstreamTarget
	}{
		{name: "new api", channelType: constant.ChannelTypeNewAPI, want: bearer},
		{name: "sub2api", channelType: constant.ChannelTypeSub2API, want: bearer},
		{name: "advanced custom query auth", channelType: constant.ChannelTypeAdvancedCustom, otherSettings: queryAuthRoute, want: func(key string) upstreamTarget {
			return upstreamTarget{Path: "/upstream/responses", QueryKey: key}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const terminal = `{"type":"response.completed","response":{"id":"first","status":"completed","usage":{"input_tokens":1000,"output_tokens":10,"total_tokens":1010}}}`
			targets := make(chan upstreamTarget, 2)
			observe := func(r *http.Request) {
				targets <- upstreamTarget{Path: r.URL.Path, Authorization: r.Header.Get("Authorization"), QueryKey: r.URL.Query().Get("api_key")}
			}
			fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(ws *websocket.Conn, r *http.Request) {
				observe(r)
				if _, _, err := ws.ReadMessage(); !assert.NoError(t, err) {
					return
				}
				if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(terminal))) {
					return
				}
				_, _, _ = ws.ReadMessage()
			})
			fixture.httpUpstream = func(w http.ResponseWriter, r *http.Request) {
				observe(r)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", terminal)
			}
			fixture.channel.SetOtherSettings(tc.otherSettings)
			require.NoError(t, model.DB.Model(fixture.channel).Updates(map[string]any{"type": tc.channelType, "settings": fixture.channel.OtherSettings}).Error)

			require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"ws-billing","input":"hi"}`)))
			assert.Equal(t, "response.completed", readResponsesWSTestEvent(t, fixture.client)["type"])
			assert.Equal(t, tc.want("upstream-first"), <-targets)

			request, err := http.NewRequest(http.MethodPost, fixture.gatewayURL+"/v1/responses", strings.NewReader(`{"model":"ws-billing","input":"hi","stream":true}`))
			require.NoError(t, err)
			request.Header.Set("Authorization", "Bearer sk-"+fixture.token.Key)
			request.Header.Set("Content-Type", "application/json")
			response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
			require.NoError(t, err)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			assert.Equal(t, http.StatusOK, response.StatusCode)
			assert.Contains(t, string(body), terminal)
			select {
			case <-fixture.httpDone:
			case <-time.After(3 * time.Second):
				t.Fatal("HTTP request did not finish")
			}
			// The polling multi-key channel rotates to its second key for the HTTP request.
			assert.Equal(t, tc.want("upstream-second"), <-targets)
			fixture.closeAndWait(t)
			assertResponsesWSAccounting(t, fixture, []int{1000, 1000})
		})
	}
}

func TestResponsesWebSocketDisconnectSettlesDeliveredOutputOnce(t *testing.T) {
	fixture := newResponsesWSBillingTest(t, `tier("output", c * 2)`, func(ws *websocket.Conn, _ *http.Request) {
		if _, _, err := ws.ReadMessage(); !assert.NoError(t, err) {
			return
		}
		for _, event := range []string{
			`{"type":"response.created","response":{"id":"partial","status":"in_progress"}}`,
			`{"type":"response.output_text.delta","delta":"hello"}`,
		} {
			if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(event))) {
				return
			}
		}
		_, _, _ = ws.ReadMessage()
	})
	require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"ws-billing","input":"hi","max_output_tokens":1}`)))
	assert.Equal(t, "response.created", readResponsesWSTestEvent(t, fixture.client)["type"])
	delta := readResponsesWSTestEvent(t, fixture.client)
	require.Equal(t, "response.output_text.delta", delta["type"])
	assert.Equal(t, "hello", delta["delta"])
	fixture.closeAndWait(t)
	assertResponsesWSAccounting(t, fixture, []int{1})
	keys, err := common.RDB.Keys(context.Background(), "perf:ws-billing:*").Result()
	require.NoError(t, err)
	assert.Empty(t, keys, "client cancellation must not affect model health")
}

func TestResponsesWebSocketCancelErrorDoesNotFinishActiveRequest(t *testing.T) {
	complete := make(chan struct{})
	defer close(complete)
	allowCreated := make(chan struct{}, 1)
	defer close(allowCreated)
	receivedCreate := make(chan struct{}, 1)
	fixture := newResponsesWSBillingTest(t, `tier("base", p * 2)`, func(ws *websocket.Conn, _ *http.Request) {
		_, create, err := ws.ReadMessage()
		if !assert.NoError(t, err) || !assert.Contains(t, string(create), `"type":"response.create"`) {
			return
		}
		receivedCreate <- struct{}{}
		<-allowCreated
		if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"active","status":"in_progress"}}`))) {
			return
		}
		_, cancel, err := ws.ReadMessage()
		if !assert.NoError(t, err) || !assert.JSONEq(t, `{"type":"response.cancel","response_id":"wrong"}`, string(cancel)) {
			return
		}
		if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"response_not_found","message":"No such response"}}`))) {
			return
		}
		<-complete
		if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"active","status":"completed","usage":{"input_tokens":1000,"output_tokens":10,"total_tokens":1010}}}`))) {
			return
		}
		_, _, _ = ws.ReadMessage()
	})
	require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"ws-billing","input":"hi"}`)))
	select {
	case <-receivedCreate:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not receive the initial request")
	}
	// Queue cancellation before the upstream accepts the response. The following
	// conflict is an acknowledgement that the client loop processed both frames.
	require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.cancel","response_id":"wrong"}`)))
	require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"ws-billing","input":"before-acceptance"}`)))
	assert.Equal(t, float64(http.StatusConflict), readResponsesWSTestEvent(t, fixture.client)["status"])
	allowCreated <- struct{}{}
	assert.Equal(t, "response.created", readResponsesWSTestEvent(t, fixture.client)["type"])
	cancelError := readResponsesWSTestEvent(t, fixture.client)
	assert.Equal(t, "error", cancelError["type"])
	assert.Equal(t, float64(http.StatusBadRequest), cancelError["status"])
	require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"ws-billing","input":"overlapping","stream_id":"other","event_id":"busy"}`)))
	conflict := readResponsesWSTestEvent(t, fixture.client)
	assert.Equal(t, "error", conflict["type"])
	assert.Equal(t, float64(http.StatusConflict), conflict["status"])
	assert.Equal(t, "other", conflict["stream_id"])
	assert.Equal(t, "busy", conflict["event_id"])
	complete <- struct{}{}
	assert.Equal(t, "response.completed", readResponsesWSTestEvent(t, fixture.client)["type"])
	fixture.closeAndWait(t)
	assertResponsesWSAccounting(t, fixture, []int{1000})
}

func TestResponsesWebSocketInitialUpstreamRejectionRefundsReservation(t *testing.T) {
	for _, tc := range []struct {
		name, upstream, wantType, wantMessage string
		status                                int
	}{
		{name: "structured error", upstream: `{"type":"error","response_id":"rejected","status":400,"error":{"type":"invalid_request_error","code":"invalid_input","message":"Invalid input"}}`, status: http.StatusBadRequest, wantType: "invalid_request_error", wantMessage: "Invalid input"},
		// A frame without an error object is still reported as a request error.
		{name: "bare error", upstream: `{"type":"error","status":500,"message":"upstream rejected"}`, status: http.StatusInternalServerError, wantType: "invalid_request_error", wantMessage: "upstream rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			preConsumed := make(chan int, 1)
			tokenID := make(chan int, 1)
			// Pre-consume no longer estimates completion tokens, so an output-priced expression reserves nothing to refund.
			fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(ws *websocket.Conn, _ *http.Request) {
				if _, _, err := ws.ReadMessage(); !assert.NoError(t, err) {
					return
				}
				var token model.Token
				if !assert.NoError(t, model.DB.First(&token, <-tokenID).Error) {
					return
				}
				preConsumed <- token.RemainQuota
				if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(tc.upstream))) {
					return
				}
				_, _, _ = ws.ReadMessage()
			})
			tokenID <- fixture.token.Id
			// Refund is asynchronous. Observe committed token writes instead of waiting
			// a fixed delay or returning while its worker still uses the test database.
			updates := make(chan struct{}, 4)
			require.NoError(t, model.DB.Callback().Update().After("gorm:commit_or_rollback_transaction").Register("responses-ws-refund", func(tx *gorm.DB) {
				if tx.Error == nil && tx.Statement.Table == "tokens" {
					select {
					case updates <- struct{}{}:
					default:
					}
				}
			}))
			require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"ws-billing","input":"hi","max_output_tokens":10}`)))
			rejection := readResponsesWSTestEvent(t, fixture.client)
			assert.Equal(t, "error", rejection["type"])
			assert.Equal(t, float64(tc.status), rejection["status"])
			rejectionError, _ := rejection["error"].(map[string]any)
			assert.Equal(t, tc.wantType, rejectionError["type"])
			assert.Equal(t, tc.wantMessage, rejectionError["message"])
			assert.Equal(t, 2000, <-preConsumed, "the rejected request reserved quota before contacting upstream")
			deadline := time.NewTimer(3 * time.Second)
			defer deadline.Stop()
			for {
				require.NoError(t, model.DB.First(fixture.token, fixture.token.Id).Error)
				if fixture.token.RemainQuota == 3000 {
					break
				}
				select {
				case <-updates:
				case <-deadline.C:
					t.Fatal("initial rejection did not refund the token reservation")
				}
			}
			fixture.closeAndWait(t)
			assertResponsesWSAccounting(t, fixture, nil)
		})
	}
}

// TEST_RESPONSES_SQL_DSN / TEST_RESPONSES_LOG_SQL_DSN optionally run these
// entry-point regressions against isolated real MySQL/PostgreSQL databases.
func TestResponsesStreamOutcomesPreserveAccounting(t *testing.T) {
	for _, transport := range []string{"websocket", "http-sse"} {
		for _, tc := range []struct {
			name, expression, terminal string
			// sseTerminal is the flat error envelope used by the Responses SSE
			// protocol; the WebSocket protocol nests the error instead.
			sseTerminal string
			delta       bool
			failed      bool
			ignored     bool
		}{
			{name: "failed-null-fixed", expression: `tier("request", fixed(0.002))`, terminal: `{"type":"response.failed","response":{"id":"first","status":"failed","usage":null,"error":{"code":"server_error","message":"sensitive upstream detail"}}}`, failed: true},
			{name: "failed-missing-fixed", expression: `tier("request", fixed(0.002))`, terminal: `{"type":"response.failed","response":{"id":"first","status":"failed"}}`, failed: true},
			{name: "failed-actual-usage", expression: `tier("input", p * 2)`, terminal: `{"type":"response.failed","response":{"id":"first","status":"failed","usage":{"input_tokens":1000,"output_tokens":10,"total_tokens":1010}}}`, failed: true},
			{name: "failed-estimated-output", expression: `tier("output", c * 2000)`, terminal: `{"type":"response.failed","response":{"id":"first","status":"failed","usage":null}}`, delta: true, failed: true},
			{name: "error-after-created", expression: `tier("request", fixed(0.002))`, terminal: `{"type":"error","status":500,"error":{"type":"server_error","code":"server_error","message":"Internal server error"}}`, sseTerminal: `{"type":"error","code":"server_error","message":"Internal server error","param":null,"sequence_number":2}`, failed: true},
			{name: "business-error-after-created", expression: `tier("request", fixed(0.002))`, terminal: `{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Input too long"}}`, sseTerminal: `{"type":"error","code":"context_length_exceeded","message":"Input too long","param":null,"sequence_number":2}`, failed: true, ignored: true},
			{name: "completed-at-output-limit", expression: `tier("request", fixed(0.002))`, terminal: `{"type":"response.incomplete","response":{"id":"first","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":1000,"output_tokens":1,"total_tokens":1001}}}`},
			{name: "completed-zero-fixed", expression: `tier("request", fixed(0.002))`, terminal: `{"type":"response.completed","response":{"id":"first","status":"completed","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`},
		} {
			t.Run(transport+"/"+tc.name, func(t *testing.T) {
				events := []string{`{"type":"response.created","response":{"id":"first","status":"in_progress"}}`}
				if tc.delta {
					events = append(events, `{"type":"response.output_text.delta","delta":"hello"}`)
				}
				terminal := tc.terminal
				if transport == "http-sse" && tc.sseTerminal != "" {
					terminal = tc.sseTerminal
				}
				events = append(events, terminal)
				fixture := newResponsesWSBillingTest(t, tc.expression, func(ws *websocket.Conn, _ *http.Request) {
					for turn := 0; ; turn++ {
						_, request, err := ws.ReadMessage()
						if err != nil {
							return
						}
						var body map[string]any
						if !assert.NoError(t, common.Unmarshal(request, &body)) {
							return
						}
						assert.Equal(t, "planner", body["stream_id"])
						assert.Equal(t, "gpt-4o", body["model"])
						output := events
						if transport == "http-sse" || turn > 0 {
							output = []string{`{"type":"response.completed","stream_id":"planner","response":{"id":"second","status":"completed","usage":{"input_tokens":1000,"output_tokens":1,"total_tokens":1001}}}`}
						}
						for _, event := range output {
							if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(event))) {
								return
							}
						}
					}
				}, events...)
				setting.ModelRequestRateLimitEnabled = true
				setting.ModelRequestRateLimitDurationMinutes = 1
				setting.ModelRequestRateLimitSuccessCount = 1
				setting.ModelRequestRateLimitCount = 0
				if transport == "http-sse" {
					request, err := http.NewRequest(http.MethodPost, fixture.gatewayURL+"/v1/responses", strings.NewReader(`{"model":"ws-billing","input":"hi","stream":true,"max_output_tokens":1}`))
					require.NoError(t, err)
					request.Header.Set("Authorization", "Bearer sk-"+fixture.token.Key)
					request.Header.Set("Content-Type", "application/json")
					response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
					require.NoError(t, err)
					body, err := io.ReadAll(response.Body)
					require.NoError(t, err)
					require.NoError(t, response.Body.Close())
					assert.Equal(t, http.StatusOK, response.StatusCode)
					assert.Contains(t, string(body), terminal)
				} else {
					require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","stream_id":"planner","model":"ws-billing","input":"hi","max_output_tokens":1}`)))
					for _, expected := range events {
						actual, err := common.Marshal(readResponsesWSTestEvent(t, fixture.client))
						require.NoError(t, err)
						assert.JSONEq(t, expected, string(actual), "upstream frames must be delivered once and unchanged")
					}
				}
				quotas := []int{1000}
				if tc.failed {
					// The first terminal must complete settlement and middleware before
					// admitting this immediately following request.
					require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","stream_id":"planner","model":"ws-billing","input":"next","max_output_tokens":1}`)))
					assert.Equal(t, "response.completed", readResponsesWSTestEvent(t, fixture.client)["type"])
					quotas = append(quotas, 1000)
				}
				require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","event_id":"limited","stream_id":"planner","model":"ws-billing","input":"limited"}`)))
				limited := readResponsesWSTestEvent(t, fixture.client)
				assert.Equal(t, float64(http.StatusTooManyRequests), limited["status"])
				assert.Equal(t, "planner", limited["stream_id"])
				assert.Equal(t, "limited", limited["event_id"])
				fixture.closeAndWait(t)
				assertResponsesWSAccounting(t, fixture, quotas)
				var logs []model.Log
				require.NoError(t, model.LOG_DB.Where("token_id = ? AND type = ?", fixture.token.Id, model.LogTypeConsume).Order("id").Find(&logs).Error)
				require.Len(t, logs, len(quotas))
				var other map[string]any
				require.NoError(t, common.UnmarshalJsonStr(logs[0].Other, &other))
				stream := other["stream_status"].(map[string]any)
				if tc.failed {
					assert.Equal(t, "error", stream["status"])
					assert.Equal(t, "failed", stream["response_status"])
				} else {
					assert.Equal(t, "ok", stream["status"])
					if tc.name == "completed-at-output-limit" {
						assert.Equal(t, "incomplete", stream["response_status"])
					} else {
						assert.Equal(t, "completed", stream["response_status"])
					}
				}
				assert.NotContains(t, logs[0].Other, "sensitive upstream detail")
				expectedRequests := int64(len(quotas))
				if tc.ignored {
					expectedRequests--
				}
				requests, successes := waitPerfCounters(t, expectedRequests)
				assert.Equal(t, expectedRequests, requests)
				assert.Equal(t, int64(1), successes)
			})
		}
	}
}

func TestResponsesHTTPHealthCountsFinalResult(t *testing.T) {
	for _, tc := range []struct {
		name, code            string
		firstStatus, attempts int
		success               bool
		ignored               bool
	}{
		{"business rejection", "context_length_exceeded", 400, 1, false, true},
		{"credentials rejected as 400", "invalid_api_key", 400, 1, false, false},
		{"retry succeeds", "server_error", 500, 2, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(*websocket.Conn, *http.Request) {})
			oldRetries := common.RetryTimes
			common.RetryTimes = 1
			t.Cleanup(func() { common.RetryTimes = oldRetries })
			var attempts atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if attempts.Add(1) == 1 {
					w.WriteHeader(tc.firstStatus)
					_, _ = fmt.Fprintf(w, `{"error":{"type":"%s","code":"%s","message":"test rejection"}}`, tc.code, tc.code)
					return
				}
				_, _ = fmt.Fprint(w, `{"id":"completed","status":"completed","usage":{"input_tokens":1000,"output_tokens":1,"total_tokens":1001}}`)
			}))
			t.Cleanup(upstream.Close)
			require.NoError(t, model.DB.Model(&model.Channel{}).Where("name = ?", "responses-ws-upstream").Update("base_url", upstream.URL).Error)
			request, err := http.NewRequest(http.MethodPost, fixture.gatewayURL+"/v1/responses", strings.NewReader(`{"model":"ws-billing","input":"hello"}`))
			require.NoError(t, err)
			request.Header.Set("Authorization", "Bearer sk-"+fixture.token.Key)
			request.Header.Set("Content-Type", "application/json")
			response, err := http.DefaultClient.Do(request)
			require.NoError(t, err)
			_, err = io.Copy(io.Discard, response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			select {
			case <-fixture.httpDone:
			case <-time.After(3 * time.Second):
				t.Fatal("HTTP request did not finish")
			}
			fixture.closeAndWait(t)
			assert.Equal(t, int64(tc.attempts), attempts.Load())
			if !tc.success {
				assert.Equal(t, tc.firstStatus, response.StatusCode)
			} else {
				assert.Equal(t, http.StatusOK, response.StatusCode)
			}
			if tc.ignored {
				keys, err := common.RDB.Keys(context.Background(), "perf:ws-billing:*").Result()
				require.NoError(t, err)
				assert.Empty(t, keys)
				return
			}
			requests, successes := waitPerfCounters(t, 1)
			assert.Equal(t, int64(1), requests)
			if tc.success {
				assert.Equal(t, int64(1), successes)
			} else {
				assert.Zero(t, successes)
			}
		})
	}
}

func TestResponsesWebSocketLocalErrorsKeepStreamIdentity(t *testing.T) {
	fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(*websocket.Conn, *http.Request) {
		t.Error("local rejection reached upstream")
	})
	require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","event_id":"invalid","stream_id":"planner","model":"ws-billing","max_output_tokens":18446744073709551615}`)))
	invalid := readResponsesWSTestEvent(t, fixture.client)
	assert.Equal(t, float64(http.StatusBadRequest), invalid["status"])
	assert.Equal(t, "planner", invalid["stream_id"])
	assert.Equal(t, "invalid", invalid["event_id"])
	fixture.token.Status = common.TokenStatusDisabled
	require.NoError(t, fixture.token.SelectUpdate())
	require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","event_id":"revoked","stream_id":"planner","model":"ws-billing","input":"hi"}`)))
	revoked := readResponsesWSTestEvent(t, fixture.client)
	assert.Equal(t, float64(http.StatusUnauthorized), revoked["status"])
	assert.Equal(t, "planner", revoked["stream_id"])
	assert.Equal(t, "revoked", revoked["event_id"])
	fixture.closeAndWait(t)
	assertResponsesWSAccounting(t, fixture, nil)
}

func TestResponsesWebSocketAmbiguousControlErrorClosesAndSettlesOnce(t *testing.T) {
	const terminal = `{"type":"error","status":500,"error":{"type":"server_error","code":"server_error","message":"Internal server error"}}`
	fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(ws *websocket.Conn, _ *http.Request) {
		if _, _, err := ws.ReadMessage(); !assert.NoError(t, err) {
			return
		}
		if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"active","status":"in_progress"}}`))) {
			return
		}
		_, cancel, err := ws.ReadMessage()
		if !assert.NoError(t, err) {
			return
		}
		assert.Contains(t, string(cancel), `"type":"response.cancel"`)
		if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(terminal))) {
			return
		}
		_, _, _ = ws.ReadMessage()
	})
	require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"ws-billing","input":"hi"}`)))
	assert.Equal(t, "response.created", readResponsesWSTestEvent(t, fixture.client)["type"])
	require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.cancel","response_id":"active"}`)))
	body, err := common.Marshal(readResponsesWSTestEvent(t, fixture.client))
	require.NoError(t, err)
	assert.JSONEq(t, terminal, string(body))
	_, _, err = fixture.client.ReadMessage()
	require.Error(t, err, "an unattributed error must close the connection instead of remaining busy")
	var timeout net.Error
	assert.False(t, errors.As(err, &timeout) && timeout.Timeout(), "the server must close before the read deadline")
	fixture.closeAndWait(t)
	assertResponsesWSAccounting(t, fixture, []int{1000})
}

// A control event queued while another is still outstanding is rejected
// locally instead of reaching upstream twice.
func TestResponsesWebSocketRejectsSecondControlWhileOnePending(t *testing.T) {
	receivedCancel := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(ws *websocket.Conn, _ *http.Request) {
		if _, _, err := ws.ReadMessage(); !assert.NoError(t, err) {
			return
		}
		if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"active","status":"in_progress"}}`))) {
			return
		}
		_, cancel, err := ws.ReadMessage()
		if !assert.NoError(t, err) || !assert.Contains(t, string(cancel), `"event_id":"first-cancel"`) {
			return
		}
		receivedCancel <- struct{}{}
		<-release
		if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"active","status":"completed","usage":{"input_tokens":1000,"output_tokens":10,"total_tokens":1010}}}`))) {
			return
		}
		_, _, _ = ws.ReadMessage()
	})
	require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"ws-billing","input":"hi"}`)))
	assert.Equal(t, "response.created", readResponsesWSTestEvent(t, fixture.client)["type"])
	require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.cancel","event_id":"first-cancel","response_id":"active"}`)))
	select {
	case <-receivedCancel:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not receive the first cancel")
	}
	require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.cancel","event_id":"second-cancel","response_id":"active"}`)))
	rejected := readResponsesWSTestEvent(t, fixture.client)
	assert.Equal(t, "error", rejected["type"])
	assert.Equal(t, float64(http.StatusBadRequest), rejected["status"])
	assert.Equal(t, "second-cancel", rejected["event_id"])
	rejectedError, _ := rejected["error"].(map[string]any)
	assert.Contains(t, rejectedError["message"], "already pending")
	release <- struct{}{}
	assert.Equal(t, "response.completed", readResponsesWSTestEvent(t, fixture.client)["type"])
	fixture.closeAndWait(t)
	assertResponsesWSAccounting(t, fixture, []int{1000})
}

// A control error resolves the outstanding control, so a corrected cancel is
// forwarded instead of being rejected as still pending.
func TestResponsesWebSocketAcceptsCancelAfterControlError(t *testing.T) {
	fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(ws *websocket.Conn, _ *http.Request) {
		if _, _, err := ws.ReadMessage(); !assert.NoError(t, err) {
			return
		}
		if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"active","status":"in_progress"}}`))) {
			return
		}
		_, cancel, err := ws.ReadMessage()
		if !assert.NoError(t, err) || !assert.JSONEq(t, `{"type":"response.cancel","response_id":"wrong"}`, string(cancel)) {
			return
		}
		if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"response_not_found","message":"No such response"}}`))) {
			return
		}
		_, cancel, err = ws.ReadMessage()
		if !assert.NoError(t, err) || !assert.JSONEq(t, `{"type":"response.cancel","response_id":"active"}`, string(cancel)) {
			return
		}
		if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.cancelled","response":{"id":"active","status":"cancelled","usage":{"input_tokens":1000,"output_tokens":0,"total_tokens":1000}}}`))) {
			return
		}
		_, _, _ = ws.ReadMessage()
	})
	require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"ws-billing","input":"hi"}`)))
	assert.Equal(t, "response.created", readResponsesWSTestEvent(t, fixture.client)["type"])
	require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.cancel","response_id":"wrong"}`)))
	cancelError := readResponsesWSTestEvent(t, fixture.client)
	assert.Equal(t, "error", cancelError["type"])
	assert.Equal(t, float64(http.StatusBadRequest), cancelError["status"])
	require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.cancel","response_id":"active"}`)))
	assert.Equal(t, "response.cancelled", readResponsesWSTestEvent(t, fixture.client)["type"])
	fixture.closeAndWait(t)
	assertResponsesWSAccounting(t, fixture, []int{1000})
}

// A late error that names the previous response must not end the request
// that follows it on the same connection, whether that response completed or
// failed.
func TestResponsesWebSocketIgnoresLateErrorForPreviousResponse(t *testing.T) {
	for _, tc := range []struct {
		name, firstTerminal, firstTerminalType string
	}{
		{name: "after completed", firstTerminalType: "response.completed", firstTerminal: `{"type":"response.completed","response":{"id":"first","status":"completed","usage":{"input_tokens":1000,"output_tokens":10,"total_tokens":1010}}}`},
		{name: "after error", firstTerminalType: "error", firstTerminal: `{"type":"error","response_id":"first","status":500,"error":{"type":"server_error","code":"server_error","message":"Internal server error"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(ws *websocket.Conn, _ *http.Request) {
				for _, turn := range [][]string{
					{`{"type":"response.created","response":{"id":"first","status":"in_progress"}}`, tc.firstTerminal},
					{
						`{"type":"error","response_id":"first","status":500,"error":{"type":"server_error","code":"server_error","message":"late failure"}}`,
						`{"type":"response.created","response":{"id":"second","status":"in_progress"}}`,
						`{"type":"response.completed","response":{"id":"second","status":"completed","usage":{"input_tokens":1000,"output_tokens":10,"total_tokens":1010}}}`,
					},
				} {
					if _, _, err := ws.ReadMessage(); !assert.NoError(t, err) {
						return
					}
					for _, event := range turn {
						if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(event))) {
							return
						}
					}
				}
				_, _, _ = ws.ReadMessage()
			})
			require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"ws-billing","input":"first"}`)))
			assert.Equal(t, "response.created", readResponsesWSTestEvent(t, fixture.client)["type"])
			assert.Equal(t, tc.firstTerminalType, readResponsesWSTestEvent(t, fixture.client)["type"])
			require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"ws-billing","input":"second"}`)))
			created := readResponsesWSTestEvent(t, fixture.client)
			require.Equal(t, "response.created", created["type"], "the late error must not reach the client or end the request: %v", created)
			assert.Equal(t, "response.completed", readResponsesWSTestEvent(t, fixture.client)["type"])
			fixture.closeAndWait(t)
			assertResponsesWSAccounting(t, fixture, []int{1000, 1000})
		})
	}
}

// An error that names a different response than the active one is relayed to
// the client and leaves the active generation running.
func TestResponsesWebSocketForwardsErrorForOtherResponseWithoutEndingRequest(t *testing.T) {
	const strayError = `{"type":"error","response_id":"other","status":500,"error":{"type":"server_error","code":"server_error","message":"unrelated failure"}}`
	fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(ws *websocket.Conn, _ *http.Request) {
		if _, _, err := ws.ReadMessage(); !assert.NoError(t, err) {
			return
		}
		for _, event := range []string{
			`{"type":"response.created","response":{"id":"active","status":"in_progress"}}`,
			strayError,
			`{"type":"response.completed","response":{"id":"active","status":"completed","usage":{"input_tokens":1000,"output_tokens":10,"total_tokens":1010}}}`,
		} {
			if !assert.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(event))) {
				return
			}
		}
		_, _, _ = ws.ReadMessage()
	})
	require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"ws-billing","input":"hi"}`)))
	assert.Equal(t, "response.created", readResponsesWSTestEvent(t, fixture.client)["type"])
	stray, err := common.Marshal(readResponsesWSTestEvent(t, fixture.client))
	require.NoError(t, err)
	assert.JSONEq(t, strayError, string(stray))
	assert.Equal(t, "response.completed", readResponsesWSTestEvent(t, fixture.client)["type"])
	fixture.closeAndWait(t)
	assertResponsesWSAccounting(t, fixture, []int{1000})
	var logs []model.Log
	require.NoError(t, model.LOG_DB.Where("type = ?", model.LogTypeConsume).Find(&logs).Error)
	require.Len(t, logs, 1)
	var other map[string]any
	require.NoError(t, common.UnmarshalJsonStr(logs[0].Other, &other))
	stream, _ := other["stream_status"].(map[string]any)
	assert.Equal(t, "ok", stream["status"])
	assert.Equal(t, "completed", stream["response_status"])
}

// Requests rejected before a RelayInfo exists reach the model health sampler
// with the HTTP classification: routing failures count against the model,
// business rejections such as a token model limit do not.
func TestResponsesWebSocketPreRoutingRejectionsFollowHealthClassification(t *testing.T) {
	require.NoError(t, i18n.Init())
	for _, tc := range []struct {
		name    string
		reject  func(*testing.T, *responsesWSBillingTest)
		status  int
		sampled bool
	}{
		{name: "no eligible channel", status: http.StatusServiceUnavailable, sampled: true, reject: func(t *testing.T, fixture *responsesWSBillingTest) {
			require.NoError(t, model.DB.Model(&model.Ability{}).Where("channel_id = ?", fixture.channel.Id).Update("enabled", false).Error)
		}},
		{name: "token model limit", status: http.StatusForbidden, reject: func(t *testing.T, fixture *responsesWSBillingTest) {
			// Update through the model so the cached token is invalidated.
			fixture.token.ModelLimitsEnabled = true
			fixture.token.ModelLimits = "other-model"
			require.NoError(t, fixture.token.Update())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newResponsesWSBillingTest(t, `tier("request", fixed(0.002))`, func(*websocket.Conn, *http.Request) {
				t.Error("rejected request reached upstream")
			})
			tc.reject(t, fixture)
			require.NoError(t, fixture.client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"ws-billing","input":"hi"}`)))
			rejected := readResponsesWSTestEvent(t, fixture.client)
			assert.Equal(t, "error", rejected["type"])
			assert.Equal(t, float64(tc.status), rejected["status"])
			fixture.closeAndWait(t)
			assertResponsesWSAccounting(t, fixture, nil)
			if !tc.sampled {
				keys, err := common.RDB.Keys(context.Background(), "perf:ws-billing:*").Result()
				require.NoError(t, err)
				assert.Empty(t, keys, "a business rejection must not affect model health")
				return
			}
			requests, successes := waitPerfCounters(t, 1)
			assert.Equal(t, int64(1), requests)
			assert.Zero(t, successes)
		})
	}
}
