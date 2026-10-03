package responsestools

import (
	"fmt"
	"strconv"
	"unicode/utf8"
)

// validateCustomArgumentUnicode prevents encoding/json's replacement of malformed
// UTF-8 or unpaired UTF-16 escapes from silently changing executable tool input.
// The existing JSON decoder still owns syntax and the single-field contract.
func validateCustomArgumentUnicode(arguments string) error {
	if !utf8.ValidString(arguments) {
		return fmt.Errorf("custom arguments contain invalid UTF-8")
	}
	for i := 0; i < len(arguments); i++ {
		if arguments[i] != '\\' {
			continue
		}
		i++
		if i >= len(arguments) || arguments[i] != 'u' || i+4 >= len(arguments) {
			continue
		}
		value, err := strconv.ParseUint(arguments[i+1:i+5], 16, 16)
		if err != nil {
			continue
		}
		i += 4
		switch {
		case value >= 0xdc00 && value <= 0xdfff:
			return fmt.Errorf("custom arguments contain an unpaired low surrogate")
		case value >= 0xd800 && value <= 0xdbff:
			if i+6 >= len(arguments) || arguments[i+1:i+3] != `\u` {
				return fmt.Errorf("custom arguments contain an unpaired high surrogate")
			}
			low, err := strconv.ParseUint(arguments[i+3:i+7], 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return fmt.Errorf("custom arguments contain an invalid surrogate pair")
			}
			i += 6
		}
	}
	return nil
}
