package main

import "testing"

func TestBuildModelEntriesMarksWebSearchModels(t *testing.T) {
	models := buildModelEntries([]byte(`{
		"models": {
			"gemini-3.1-flash-lite": {
				"displayName": "Gemini 3.1 Flash Lite",
				"maxTokens": 1048576,
				"maxOutputTokens": 65535
			},
			"gemini-3-flash-agent": {
				"displayName": "Gemini 3 Flash Agent"
			}
		},
		"webSearchModelIds": ["gemini-3.1-flash-lite"]
	}`))

	var webSearchModel, agentModel *modelEntry
	for i := range models {
		switch models[i].ID {
		case "gemini-3.1-flash-lite":
			webSearchModel = &models[i]
		case "gemini-3-flash-agent":
			agentModel = &models[i]
		}
	}
	if webSearchModel == nil {
		t.Fatal("gemini-3.1-flash-lite entry missing")
	}
	if !webSearchModel.SupportsWebSearch {
		t.Fatal("gemini-3.1-flash-lite should be marked supports_web_search")
	}
	if webSearchModel.ContextLength != 1048576 || webSearchModel.MaxCompletionTokens != 65535 {
		t.Fatalf("token limits not preserved: %#v", webSearchModel)
	}
	if agentModel == nil {
		t.Fatal("gemini-3-flash-agent entry missing")
	}
	if agentModel.SupportsWebSearch {
		t.Fatal("gemini-3-flash-agent should not be marked supports_web_search")
	}
}
