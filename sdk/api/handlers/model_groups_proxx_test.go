package handlers

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	proxxgroups "github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/groups"
)

// A configured group named "auto" must win over upstream's ResolveAutoModel, and the group name
// must reach the auth manager unchanged with the synthetic group provider.
func TestProxxGroupRoutingHookWinsOverAutoResolution(t *testing.T) {
	t.Cleanup(func() { proxxgroups.Apply(nil) })
	proxxgroups.Apply(&config.Config{Proxx: config.ProxxConfig{ModelGroups: []config.ProxxModelGroup{{Name: "auto", Models: []string{"some-model"}}}}})
	h := &BaseAPIHandler{}
	for _, model := range []string{"auto", "auto(high)"} {
		providers, normalized, errMsg := h.getRequestDetailsWithOptions(model, false, false)
		if errMsg != nil {
			t.Fatalf("%s: error = %v", model, errMsg.Error)
		}
		if !reflect.DeepEqual(providers, []string{proxxgroups.Provider}) || normalized != model {
			t.Fatalf("%s: providers=%v normalized=%q", model, providers, normalized)
		}
	}
	proxxgroups.Apply(nil)
	if providers, _, _ := h.getRequestDetailsWithOptions("auto", false, false); reflect.DeepEqual(providers, []string{proxxgroups.Provider}) {
		t.Fatal("without a group, auto must keep upstream resolution")
	}
}
