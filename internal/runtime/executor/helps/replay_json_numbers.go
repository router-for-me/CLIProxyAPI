package helps

import (
	"encoding/json"
	"math/big"
	"strings"
)

// NormalizeReplayJSONNumbers normalizes json.Number values in a decoded JSON
// tree for exact replay comparisons. It treats equivalent decimal/exponent
// spellings alike without rounding through float64 or expanding enormous exponents.
func NormalizeReplayJSONNumbers(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			typed[key] = NormalizeReplayJSONNumbers(child)
		}
	case []any:
		for index, child := range typed {
			typed[index] = NormalizeReplayJSONNumbers(child)
		}
	case json.Number:
		return normalizeReplayJSONNumber(typed)
	}
	return value
}

func normalizeReplayJSONNumber(number json.Number) json.Number {
	mantissa := number.String()
	sign := ""
	if strings.HasPrefix(mantissa, "-") {
		sign, mantissa = "-", mantissa[1:]
	}
	var exponent big.Int
	if index := strings.IndexAny(mantissa, "eE"); index >= 0 {
		exponentText := mantissa[index+1:]
		negative := strings.HasPrefix(exponentText, "-")
		if negative || strings.HasPrefix(exponentText, "+") {
			exponentText = exponentText[1:]
		}
		exponentText = strings.TrimLeft(exponentText, "0")
		// Leave pathological exponents unchanged so replay comparisons fail closed
		// without constructing an unbounded big integer from client input.
		if len(exponentText) > 64 {
			return number
		}
		if exponentText == "" {
			exponentText = "0"
		}
		if _, ok := exponent.SetString(exponentText, 10); !ok {
			return number
		}
		if negative {
			exponent.Neg(&exponent)
		}
		mantissa = mantissa[:index]
	}
	if point := strings.IndexByte(mantissa, '.'); point >= 0 {
		exponent.Sub(&exponent, big.NewInt(int64(len(mantissa)-point-1)))
		mantissa = mantissa[:point] + mantissa[point+1:]
	}
	digits := strings.TrimLeft(mantissa, "0")
	if digits == "" {
		return json.Number("0")
	}
	trimmed := strings.TrimRight(digits, "0")
	exponent.Add(&exponent, big.NewInt(int64(len(digits)-len(trimmed))))
	digits = trimmed
	exponent.Add(&exponent, big.NewInt(int64(len(digits)-1)))

	// Keep ordinary magnitudes in decimal form, as encoding/json does. Other
	// magnitudes stay compact even when their exponent exceeds the int64 range.
	if magnitude := exponent.Int64(); exponent.IsInt64() && magnitude >= -6 && magnitude < 21 {
		point := int(magnitude) + 1
		switch {
		case point <= 0:
			return json.Number(sign + "0." + strings.Repeat("0", -point) + digits)
		case point >= len(digits):
			return json.Number(sign + digits + strings.Repeat("0", point-len(digits)))
		default:
			return json.Number(sign + digits[:point] + "." + digits[point:])
		}
	}
	mantissa = digits[:1]
	if len(digits) > 1 {
		mantissa += "." + digits[1:]
	}
	exponentSign := ""
	if exponent.Sign() >= 0 {
		exponentSign = "+"
	}
	return json.Number(sign + mantissa + "e" + exponentSign + exponent.String())
}
