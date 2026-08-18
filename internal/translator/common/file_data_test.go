package common

import "testing"

func TestNormalizeOpenAIFileData(t *testing.T) {
	tests := []struct {
		name         string
		filename     string
		fallbackMIME string
		fileData     string
		wantMIMEType string
		wantData     string
		wantOK       bool
	}{
		{
			name:         "data URL",
			filename:     "test.pdf",
			fileData:     "data:application/pdf;base64,JVBERi0xLjQK",
			wantMIMEType: "application/pdf",
			wantData:     "JVBERi0xLjQK",
			wantOK:       true,
		},
		{
			name:         "data URL metadata and MIME override",
			filename:     "test.txt",
			fileData:     "data:application/pdf;charset=binary;BASE64,JVBERi0xLjQK",
			wantMIMEType: "application/pdf",
			wantData:     "JVBERi0xLjQK",
			wantOK:       true,
		},
		{
			name:         "case-insensitive data URL scheme",
			filename:     "test.pdf",
			fileData:     "DATA:application/pdf;base64,JVBERi0xLjQK",
			wantMIMEType: "application/pdf",
			wantData:     "JVBERi0xLjQK",
			wantOK:       true,
		},
		{
			name:         "raw base64",
			filename:     "TEST.PDF",
			fileData:     "JVBERi0xLjQK",
			wantMIMEType: "application/pdf",
			wantData:     "JVBERi0xLjQK",
			wantOK:       true,
		},
		{
			name:         "raw base64 with explicit MIME type",
			fallbackMIME: "application/pdf",
			fileData:     "JVBERi0xLjQK",
			wantMIMEType: "application/pdf",
			wantData:     "JVBERi0xLjQK",
			wantOK:       true,
		},
		{name: "empty data", filename: "test.pdf"},
		{name: "raw base64 without known extension", filename: "test", fileData: "JVBERi0xLjQK"},
		{name: "data URL without base64 marker", filename: "test.pdf", fileData: "data:application/pdf,JVBERi0xLjQK"},
		{name: "data URL without MIME type", filename: "test.pdf", fileData: "data:;base64,JVBERi0xLjQK"},
		{name: "data URL without payload", filename: "test.pdf", fileData: "data:application/pdf;base64,"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mimeType, data, ok := NormalizeOpenAIFileData(test.filename, test.fallbackMIME, test.fileData)
			if mimeType != test.wantMIMEType || data != test.wantData || ok != test.wantOK {
				t.Fatalf("NormalizeOpenAIFileData() = (%q, %q, %v), want (%q, %q, %v)", mimeType, data, ok, test.wantMIMEType, test.wantData, test.wantOK)
			}
		})
	}
}
