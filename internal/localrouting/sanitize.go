package localrouting

import (
	"regexp"
	"strconv"
	"strings"
)

var invalidLabelChars = regexp.MustCompile(`[^a-z0-9-]+`)

func NormalizeTLD(raw string) string {
	tld := strings.ToLower(strings.TrimSpace(raw))
	tld = strings.TrimPrefix(tld, ".")
	if tld == "" {
		return "localhost"
	}
	return invalidLabelChars.ReplaceAllString(tld, "")
}

func SanitizeLabel(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	v = strings.ReplaceAll(v, "_", "-")
	v = invalidLabelChars.ReplaceAllString(v, "-")
	v = strings.Trim(v, "-")
	if v == "" {
		return "cliproxyapi"
	}
	if len(v) > 63 {
		v = strings.Trim(v[:63], "-")
		if v == "" {
			v = "cliproxyapi"
		}
	}
	return v
}

func BuildHost(name, tld string) string {
	name = SanitizeLabel(name)
	tld = NormalizeTLD(tld)
	if tld == "" {
		return name
	}
	return name + "." + tld
}

func BuildURL(https bool, host string, edgePort int) string {
	scheme := "http"
	if https {
		scheme = "https"
	}
	if edgePort <= 0 {
		edgePort = DefaultEdgePort
	}
	if (https && edgePort == 443) || (!https && edgePort == 80) {
		return scheme + "://" + host
	}
	return scheme + "://" + host + ":" + strconv.Itoa(edgePort)
}
