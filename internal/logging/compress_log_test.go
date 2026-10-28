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
		"model":     "gpt-6-luna",
		"combo":     "standard",
		"pipeline":  "dedup,rtk,lite",
		"engines":   "rtk=2610,lite=130",
		"segments":  7,
		"style":     "",
		"style_len": 0,
		"bytes_in":  10800,
		"bytes_out": 2821,
		"saved_pct": 74,
	}).Info("payload compression applied")

	line := buf.String()
	for _, want := range []string{
		"model=gpt-6-luna",
		`combo=standard`,
		`pipeline="dedup,rtk,lite"`,
		`engines="rtk=2610,lite=130"`,
		"segments=7",
		"bytes_in=10800",
		"bytes_out=2821",
		"saved_pct=74",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("missing %q in:\n%s", want, line)
		}
	}
}
