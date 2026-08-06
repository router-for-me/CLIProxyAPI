package main

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestShouldProcessModelPriority(t *testing.T) {
	cfg := config{
		ForceTextOnlyModels:   []string{"forced"},
		ForceMultimodalModels: []string{"forced", "vision"},
		UnknownModelPolicy:    "bypass",
	}
	if !shouldProcessModel(interceptRequest{RequestInterceptRequest: pluginapi.RequestInterceptRequest{Model: "forced", ModelInputModalities: []string{"image"}}}, cfg) {
		t.Fatal("text-only force rule must win")
	}
	if shouldProcessModel(interceptRequest{RequestInterceptRequest: pluginapi.RequestInterceptRequest{Model: "vision", ModelInputModalities: []string{"text"}}}, cfg) {
		t.Fatal("multimodal force rule must bypass")
	}
	if shouldProcessModel(interceptRequest{RequestInterceptRequest: pluginapi.RequestInterceptRequest{Model: "registered", ModelInputModalities: []string{"text", "image"}}}, cfg) {
		t.Fatal("registered image modality must bypass")
	}
	if !shouldProcessModel(interceptRequest{RequestInterceptRequest: pluginapi.RequestInterceptRequest{Model: "text", ModelInputModalities: []string{"text"}}}, cfg) {
		t.Fatal("known text-only model must be processed")
	}
}

func TestValidateImageURLRejectsNonPortableReferences(t *testing.T) {
	for _, value := range []string{"file_123", "gs://bucket/image.png", "data:audio/wav;base64,AAAA"} {
		_, err := validateImageURL(value)
		if err == nil {
			t.Fatalf("validateImageURL(%q) error = nil", value)
		}
		if got := errorStatus(err); got != http.StatusBadRequest {
			t.Fatalf("validateImageURL(%q) status = %d", value, got)
		}
	}
	for _, value := range []string{"https://example.com/image.png", "data:image/png;base64,AAAA"} {
		if _, err := validateImageURL(value); err != nil {
			t.Fatalf("validateImageURL(%q) error = %v", value, err)
		}
	}
}
