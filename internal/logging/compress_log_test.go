package logging

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

func TestFormatterPrintsCompressionFields(t *testing.T) {
	var buf bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&buf)
	logger.SetLevel(logrus.InfoLevel)
	logger.SetFormatter(&LogFormatter{})
	logger.WithFields(logrus.Fields{
		"model":        "gpt-6-luna",
		"combo":        "standard",
		"pipeline":     "dedup,rtk,lite",
		"engines":      "rtk=653,lite=33",
		"steps":        "lite:2560>2500,rtk:2560>2000",
		"lite_passes":  "whitespace=12,tool-cap=3",
		"segments":     7,
		"style":        "",
		"style_tokens": 0,
		"tokens_in":    2700,
		"tokens_out":   706,
		"saved_pct":    74,
	}).Info("payload compression applied")

	line := buf.String()
	for _, want := range []string{
		"model=gpt-6-luna",
		`combo=standard`,
		`pipeline="dedup,rtk,lite"`,
		`engines="rtk=653,lite=33"`,
		`steps="lite:2560>2500,rtk:2560>2000"`,
		`lite_passes="whitespace=12,tool-cap=3"`,
		"segments=7",
		"tokens_in=2700",
		"tokens_out=706",
		"saved_pct=74",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("missing %q in:\n%s", want, line)
		}
	}
}
