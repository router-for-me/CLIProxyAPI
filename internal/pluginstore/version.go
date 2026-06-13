package pluginstore

import (
	"strconv"
	"strings"
)

// UpdateAvailable reports whether latest should be offered as an upgrade over
// installed. A leading "v"/"V" is ignored on both sides. Versions are compared
// numerically when both are dotted release numbers, so an installed version
// newer than the registry one is not reported as an update; otherwise any
// difference counts as an update.
func UpdateAvailable(installed, latest string) bool {
	installed = normalizeVersion(installed)
	latest = normalizeVersion(latest)
	if installed == "" || latest == "" || installed == latest {
		return false
	}
	comparison, comparable := compareVersions(installed, latest)
	if !comparable {
		return true
	}
	return comparison < 0
}

func normalizeVersion(version string) string {
	version = strings.TrimSpace(version)
	if len(version) > 1 && (version[0] == 'v' || version[0] == 'V') {
		version = version[1:]
	}
	return version
}

// compareVersions compares dotted numeric versions segment by segment, with
// missing segments treated as zero. It reports false when either version
// contains a non-numeric segment.
func compareVersions(a, b string) (int, bool) {
	segmentsA := strings.Split(a, ".")
	segmentsB := strings.Split(b, ".")
	length := len(segmentsA)
	if len(segmentsB) > length {
		length = len(segmentsB)
	}
	for index := 0; index < length; index++ {
		numberA, okA := versionSegment(segmentsA, index)
		numberB, okB := versionSegment(segmentsB, index)
		if !okA || !okB {
			return 0, false
		}
		if numberA != numberB {
			if numberA < numberB {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func versionSegment(segments []string, index int) (int64, bool) {
	if index >= len(segments) {
		return 0, true
	}
	number, errParse := strconv.ParseInt(segments[index], 10, 64)
	if errParse != nil || number < 0 {
		return 0, false
	}
	return number, true
}
