package ccsadvisor

import "errors"

// ValidateDeclaration checks client input before the transport commits headers.
func ValidateDeclaration(req Message) error {
	_, _, err := declaration(list(req["tools"]))
	return err
}

func declaration(tools []any) (int, int, error) {
	idx, limit := -1, 2
	for i, v := range tools {
		d := obj(v)
		if d["type"] != "advisor_20260301" || d["name"] != "advisor" {
			continue
		}
		if idx >= 0 {
			return -1, 0, errors.New("duplicate advisor declaration")
		}
		idx = i
		if value, present := d["max_uses"]; present {
			n, ok := counter(value)
			if !ok || n <= 0 {
				return -1, 0, errors.New("invalid advisor max_uses")
			}
			if n == 1 {
				limit = 1
			}
		}
	}
	return idx, limit, nil
}
