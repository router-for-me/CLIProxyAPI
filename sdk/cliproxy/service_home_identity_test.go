package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestHomeOverlayKeepsLocalServerID(t *testing.T) {
	const localID = "e8bd1fce-3d81-4e40-9589-17b8709159eb"
	const suppliedID = "8237fe4a-1419-4a20-8b39-77dbc44168cc"
	s := &Service{cfg: &config.Config{ServerID: localID}}
	s.cfg.Home.Enabled = true
	client, _ := newHomePluginTaskTestClient(t, nil, 0)
	for _, payload := range []string{
		"server: {port: 8317}\nrequest-retry: 3\n",
		"server: {port: 8317, server-id: " + suppliedID + "}\nrequest-retry: 3\n",
	} {
		remote, errParse := config.ParseConfigBytes([]byte(payload))
		if errParse != nil {
			t.Fatal(errParse)
		}
		work, errStage := s.stageHomeOverlayWithClient(context.Background(), remote, client)
		if errStage != nil {
			t.Fatal(errStage)
		}
		if work.config.ServerID != localID || work.config.RequestRetry != 3 {
			t.Fatalf("merged server ID=%q and retries=%d", work.config.ServerID, work.config.RequestRetry)
		}
	}
}
