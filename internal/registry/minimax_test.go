package registry

import "testing"

func TestGetMiniMaxModels(t *testing.T) {
	models := GetMiniMaxModels()
	if len(models) == 0 {
		t.Fatal("expected at least one MiniMax model")
	}

	hasM27 := false
	hasM27Highspeed := false
	for _, m := range models {
		if m.ID == "MiniMax-M2.7" {
			hasM27 = true
			if m.OwnedBy != "minimax" {
				t.Errorf("expected owned_by=minimax, got %s", m.OwnedBy)
			}
			if m.Type != "minimax" {
				t.Errorf("expected type=minimax, got %s", m.Type)
			}
		}
		if m.ID == "MiniMax-M2.7-highspeed" {
			hasM27Highspeed = true
		}
	}
	if !hasM27 {
		t.Error("expected MiniMax-M2.7 in model list")
	}
	if !hasM27Highspeed {
		t.Error("expected MiniMax-M2.7-highspeed in model list")
	}
}

func TestGetStaticModelDefinitionsByChannelMiniMax(t *testing.T) {
	models := GetStaticModelDefinitionsByChannel("minimax")
	if len(models) == 0 {
		t.Fatal("expected models for minimax channel")
	}
}

func TestLookupStaticModelInfoMiniMax(t *testing.T) {
	m := LookupStaticModelInfo("MiniMax-M2.7")
	if m == nil {
		t.Fatal("expected to find MiniMax-M2.7 in static model info")
	}
	if m.ID != "MiniMax-M2.7" {
		t.Errorf("expected ID=MiniMax-M2.7, got %s", m.ID)
	}

	m2 := LookupStaticModelInfo("MiniMax-M2.7-highspeed")
	if m2 == nil {
		t.Fatal("expected to find MiniMax-M2.7-highspeed in static model info")
	}
}
