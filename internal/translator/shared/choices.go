package shared

import (
    "github.com/tidwall/gjson"
    "github.com/tidwall/sjson"
)

// ReplicateChoices ensures the choices array contains indices [0..n-1].
// It replicates the first choice when upstream returned fewer than n results.
// The provided JSON string is expected to have a choices array with at
// least one element. When preconditions are not met or n <= 1, the input
// JSON is returned unchanged.
func ReplicateChoices(jsonStr string, n int) string {
    if n <= 1 {
        return jsonStr
    }
    ch := gjson.Get(jsonStr, "choices")
    if !ch.Exists() || !ch.IsArray() {
        return jsonStr
    }
    if int(ch.Get("#").Int()) == 0 {
        return jsonStr
    }

    base := gjson.Get(jsonStr, "choices.0").Raw
    baseWithIndex, _ := sjson.Set(base, "index", 0)
    out, _ := sjson.SetRaw(jsonStr, "choices.0", baseWithIndex)
    for i := 1; i < n; i++ {
        copyWithIndex, _ := sjson.Set(baseWithIndex, "index", i)
        out, _ = sjson.SetRaw(out, "choices.-1", copyWithIndex)
    }
    return out
}

