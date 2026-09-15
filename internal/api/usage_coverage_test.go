package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type coverageReviewSink struct {
	model   string
	records []usage.Record
}

func (*coverageReviewSink) Synchronous() bool { return true }
func (s *coverageReviewSink) HandleUsage(_ context.Context, r usage.Record) {
	if r.Model == s.model || r.ExecutorType == "EndpointCoverage" && strings.HasSuffix(r.Endpoint, "/"+s.model) {
		s.records = append(s.records, r)
	}
}
func TestCoverageSurvivesNormalHandlerContext(t *testing.T) {
	sink := &coverageReviewSink{model: t.Name()}
	usage.RegisterPlugin(sink)
	router := gin.New()
	router.Use(usageCoverageMiddleware(), AuthMiddleware(nil))
	router.POST("/v1/"+t.Name(), func(c *gin.Context) {
		h := &handlers.BaseAPIHandler{Cfg: &sdkconfig.SDKConfig{}}
		ctx, cancel := h.GetContextWithCancel(nil, c, context.Background())
		defer cancel()
		usage.PublishRecord(ctx, usage.Record{Model: t.Name()})
		c.Status(http.StatusOK)
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/"+t.Name(), nil))
	if len(sink.records) != 1 {
		t.Fatalf("measured operation emitted %d records, want 1", len(sink.records))
	}
	if sink.records[0].GenerationID == "" {
		t.Fatal("executor lost generation identity")
	}
}

func TestCoverageSkipsRejectedAndUnmatchedRequests(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		matched bool
		want    int
	}{
		{"unauthorized", 401, true, 0}, {"forbidden", 403, true, 0}, {"unmatched", 404, false, 0}, {"gate_unavailable", 503, true, 0}, {"gate_rate_limited", 429, true, 0}, {"accepted_control", 200, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &coverageReviewSink{model: t.Name()}
			usage.RegisterPlugin(sink)
			router := gin.New()
			router.Use(usageCoverageMiddleware())
			path := "/v1/" + t.Name()
			if tc.matched {
				router.POST(path, func(c *gin.Context) {
					if tc.status >= 400 {
						c.AbortWithStatus(tc.status)
					}
				}, AuthMiddleware(nil), func(c *gin.Context) { c.Status(tc.status) })
			}
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, nil))
			if len(sink.records) != tc.want {
				t.Fatalf("record count=%d, want %d", len(sink.records), tc.want)
			}
		})
	}
}

func TestCoverageSkipsHomeHeartbeatRejection(t *testing.T) {
	previous := home.Current()
	home.SetCurrent(nil)
	t.Cleanup(func() { home.SetCurrent(previous) })
	sink := &coverageReviewSink{model: t.Name()}
	usage.RegisterPlugin(sink)
	server := &Server{cfg: &config.Config{Home: config.HomeConfig{Enabled: true}}}
	router := gin.New()
	router.Use(usageCoverageMiddleware(), server.homeHeartbeatMiddleware(), AuthMiddleware(nil))
	path := "/v1/" + t.Name()
	router.POST(path, func(c *gin.Context) { t.Fatal("handler ran while heartbeat was unavailable") })
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
	if recorder.Code != http.StatusServiceUnavailable || len(sink.records) != 0 {
		t.Fatalf("heartbeat rejection: status=%d records=%d", recorder.Code, len(sink.records))
	}
}

func TestCoverageRetainsAcceptedHandlerFailures(t *testing.T) {
	sink := &coverageReviewSink{model: t.Name()}
	usage.RegisterPlugin(sink)
	router := gin.New()
	router.Use(usageCoverageMiddleware(), AuthMiddleware(nil))
	path := "/v1/" + t.Name()
	router.POST(path, func(c *gin.Context) { c.AbortWithStatus(http.StatusServiceUnavailable) })
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, nil))
	if len(sink.records) != 1 || !sink.records[0].Failed {
		t.Fatalf("accepted handler failure lost: %+v", sink.records)
	}
}
