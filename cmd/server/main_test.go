package main

import (
	"reflect"
	"testing"
)

func TestFilterBackgroundArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "remove background flag only",
			args: []string{"--background", "--config", "config.yaml"},
			want: []string{"--config", "config.yaml"},
		},
		{
			name: "remove background value true",
			args: []string{"--background", "true", "--config", "config.yaml"},
			want: []string{"--config", "config.yaml"},
		},
		{
			name: "remove background value false short form",
			args: []string{"-background", "f", "--config", "config.yaml"},
			want: []string{"--config", "config.yaml"},
		},
		{
			name: "remove background equals syntax",
			args: []string{"--background=1", "--config", "config.yaml"},
			want: []string{"--config", "config.yaml"},
		},
		{
			name: "keep non bool next token",
			args: []string{"--background", "--config", "config.yaml"},
			want: []string{"--config", "config.yaml"},
		},
		{
			name: "keep other flags",
			args: []string{"--local-model", "--config", "config.yaml"},
			want: []string{"--local-model", "--config", "config.yaml"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := filterBackgroundArgs(tt.args)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("filterBackgroundArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}

