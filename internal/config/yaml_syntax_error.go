package config

import (
	"regexp"
	"strconv"

	"gopkg.in/yaml.v3"
)

var yamlSyntaxPrefix = regexp.MustCompile(`^yaml: (?:line (\d+): )?`)

// Larger bodies keep yaml.v3's message, so the search costs at most a few
// parses of a normal-sized config.
const maxYAMLSyntaxSearchBytes = 1 << 20

// DescribeYAMLSyntaxError restates err, from yaml.Unmarshal(body), with the
// one-based line of the problem. yaml.v3's own line can be zero-based, missing,
// or the start of the enclosing block, but never past the problem, so it is the
// lower bound when searching for the shortest line prefix that fails the same way.
func DescribeYAMLSyntaxError(body []byte, err error) string {
	match := yamlSyntaxPrefix.FindStringSubmatch(err.Error())
	if match == nil || len(body) > maxYAMLSyntaxSearchBytes {
		return err.Error()
	}
	problem := err.Error()[len(match[0]):]
	// ends[i] is the offset just past line i+1.
	var ends []int
	for i := 0; i < len(body); i++ {
		if body[i] == '\n' || body[i] == '\r' && (i+1 == len(body) || body[i+1] != '\n') {
			ends = append(ends, i+1)
		}
	}
	if len(ends) == 0 || ends[len(ends)-1] != len(body) {
		ends = append(ends, len(body))
	}
	fails := func(lines int) bool {
		var node yaml.Node
		errPrefix := yaml.Unmarshal(body[:ends[lines-1]], &node)
		if errPrefix == nil {
			return false
		}
		prefixMatch := yamlSyntaxPrefix.FindStringSubmatch(errPrefix.Error())
		return prefixMatch != nil && errPrefix.Error()[len(prefixMatch[0]):] == problem
	}
	low, high := 1, len(ends)
	if reported, errAtoi := strconv.Atoi(match[1]); errAtoi == nil && reported > low && reported <= high {
		low = reported
	}
	if !fails(high) {
		return err.Error()
	}
	for low < high {
		if middle := (low + high) / 2; fails(middle) {
			high = middle
		} else {
			low = middle + 1
		}
	}
	return "yaml: line " + strconv.Itoa(high) + ": " + problem
}
