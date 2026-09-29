package service

import (
	"math"
	"strings"
	"sync"
	"unicode"
)

// Provider 定义模型厂商大类
type Provider string

const (
	OpenAI  Provider = "openai"  // 代表 GPT-3.5, GPT-4, GPT-4o
	Gemini  Provider = "gemini"  // 代表 Gemini 1.0, 1.5 Pro/Flash
	Claude  Provider = "claude"  // 代表 Claude 3, 3.5 Sonnet
	Unknown Provider = "unknown" // 兜底默认
)

// multipliers 定义不同厂商的计费权重
type multipliers struct {
	Word       float64 // 英文单词 (每词)
	Number     float64 // 数字 (每连续数字串)
	CJK        float64 // 中日韩字符 (每字)
	Symbol     float64 // 普通标点符号 (每个)
	MathSymbol float64 // 数学符号 (∑,∫,∂,√等，每个)
	URLDelim   float64 // URL分隔符 (/,:,?,&,=,#,%) - tokenizer优化好
	AtSign     float64 // @符号 - 导致单词切分，消耗较高
	Emoji      float64 // Emoji表情 (每个)
	Newline    float64 // 换行符/制表符 (每个)
	Space      float64 // 空格 (每个)
	BasePad    int     // 基础起步消耗 (Start/End tokens)
}

var (
	multipliersMap = map[Provider]multipliers{
		Gemini: {
			Word: 1.15, Number: 2.8, CJK: 0.68, Symbol: 0.38, MathSymbol: 1.05, URLDelim: 1.2, AtSign: 2.5, Emoji: 1.08, Newline: 1.15, Space: 0.2, BasePad: 0,
		},
		Claude: {
			Word: 1.13, Number: 1.63, CJK: 1.21, Symbol: 0.4, MathSymbol: 4.52, URLDelim: 1.26, AtSign: 2.82, Emoji: 2.6, Newline: 0.89, Space: 0.39, BasePad: 0,
		},
		OpenAI: {
			Word: 1.02, Number: 1.55, CJK: 0.85, Symbol: 0.4, MathSymbol: 2.68, URLDelim: 1.0, AtSign: 2.0, Emoji: 2.12, Newline: 0.5, Space: 0.42, BasePad: 0,
		},
	}
	multipliersLock sync.RWMutex
)

// getMultipliers 根据厂商获取权重配置
func getMultipliers(p Provider) multipliers {
	multipliersLock.RLock()
	defer multipliersLock.RUnlock()

	switch p {
	case Gemini:
		return multipliersMap[Gemini]
	case Claude:
		return multipliersMap[Claude]
	case OpenAI:
		return multipliersMap[OpenAI]
	default:
		// 默认兜底 (按 OpenAI 的算)
		return multipliersMap[OpenAI]
	}
}

// EstimateToken 计算 Token 数量
func EstimateToken(provider Provider, text string) int {
	m := getMultipliers(provider)
	var count float64

	// 状态机变量
	type WordType int
	const (
		None WordType = iota
		Latin
		Number
	)
	currentWordType := None

	for _, r := range text {
		// 1. 处理空格和换行符
		if unicode.IsSpace(r) {
			currentWordType = None
			// 换行符和制表符使用Newline权重
			if r == '\n' || r == '\t' {
				count += m.Newline
			} else {
				// 普通空格使用Space权重
				count += m.Space
			}
			continue
		}

		// 2. 处理 CJK (中日韩) - 按字符计费
		if isCJK(r) {
			currentWordType = None
			count += m.CJK
			continue
		}

		// 3. 处理Emoji - 使用专门的Emoji权重
		if isEmoji(r) {
			currentWordType = None
			count += m.Emoji
			continue
		}

		// 4. 处理拉丁字母/数字 (英文单词)
		if isLatinOrNumber(r) {
			isNum := unicode.IsNumber(r)
			newType := Latin
			if isNum {
				newType = Number
			}

			// 如果之前不在单词中，或者类型发生变化（字母<->数字），则视为新token
			// 注意：对于OpenAI，通常"version 3.5"会切分，"abc123xyz"有时也会切分
			// 这里简单起见，字母和数字切换时增加权重
			if currentWordType == None || currentWordType != newType {
				if newType == Number {
					count += m.Number
				} else {
					count += m.Word
				}
				currentWordType = newType
			}
			// 单词中间的字符不额外计费
			continue
		}

		// 5. 处理标点符号/特殊字符 - 按类型使用不同权重
		currentWordType = None
		if isMathSymbol(r) {
			count += m.MathSymbol
		} else if r == '@' {
			count += m.AtSign
		} else if isURLDelim(r) {
			count += m.URLDelim
		} else {
			count += m.Symbol
		}
	}

	// 向上取整并加上基础 padding
	return int(math.Ceil(count)) + m.BasePad
}

// 辅助：判断是否为 CJK 字符
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		(r >= 0x3040 && r <= 0x30FF) || // 日文
		(r >= 0xAC00 && r <= 0xD7A3) // 韩文
}

// 辅助：判断是否为单词主体 (字母或数字)
func isLatinOrNumber(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r)
}

// 辅助：判断是否为Emoji字符
func isEmoji(r rune) bool {
	// Emoji的Unicode范围
	// 基本范围：0x1F300-0x1F9FF (Emoticons, Symbols, Pictographs)
	// 补充范围：0x2600-0x26FF (Misc Symbols), 0x2700-0x27BF (Dingbats)
	// 表情符号：0x1F600-0x1F64F (Emoticons)
	// 其他：0x1F900-0x1F9FF (Supplemental Symbols and Pictographs)
	return (r >= 0x1F300 && r <= 0x1F9FF) ||
		(r >= 0x2600 && r <= 0x26FF) ||
		(r >= 0x2700 && r <= 0x27BF) ||
		(r >= 0x1F600 && r <= 0x1F64F) ||
		(r >= 0x1F900 && r <= 0x1F9FF) ||
		(r >= 0x1FA00 && r <= 0x1FAFF) // Symbols and Pictographs Extended-A
}

// 辅助：判断是否为数学符号
func isMathSymbol(r rune) bool {
	// 数学运算符和符号
	// 基本数学符号：∑ ∫ ∂ √ ∞ ≤ ≥ ≠ ≈ ± × ÷
	// 上下标数字：² ³ ¹ ⁴ ⁵ ⁶ ⁷ ⁸ ⁹ ⁰
	// 希腊字母等也常用于数学
	mathSymbols := "∑∫∂√∞≤≥≠≈±×÷∈∉∋∌⊂⊃⊆⊇∪∩∧∨¬∀∃∄∅∆∇∝∟∠∡∢°′″‴⁺⁻⁼⁽⁾ⁿ₀₁₂₃₄₅₆₇₈₉₊₋₌₍₎²³¹⁴⁵⁶⁷⁸⁹⁰"
	for _, m := range mathSymbols {
		if r == m {
			return true
		}
	}
	// Mathematical Operators (U+2200–U+22FF)
	if r >= 0x2200 && r <= 0x22FF {
		return true
	}
	// Supplemental Mathematical Operators (U+2A00–U+2AFF)
	if r >= 0x2A00 && r <= 0x2AFF {
		return true
	}
	// Mathematical Alphanumeric Symbols (U+1D400–U+1D7FF)
	if r >= 0x1D400 && r <= 0x1D7FF {
		return true
	}
	return false
}

// 辅助：判断是否为URL分隔符（tokenizer对这些优化较好）
func isURLDelim(r rune) bool {
	// URL中常见的分隔符，tokenizer通常优化处理
	urlDelims := "/:?&=;#%"
	for _, d := range urlDelims {
		if r == d {
			return true
		}
	}
	return false
}

func EstimateTokenByModel(model, text string) int {
	// strings.Contains(model, "gpt-4o")
	if text == "" {
		return 0
	}

	model = strings.ToLower(model)
	if strings.Contains(model, "gemini") {
		return EstimateToken(Gemini, text)
	} else if strings.Contains(model, "claude") {
		return EstimateToken(Claude, text)
	} else {
		return EstimateToken(OpenAI, text)
	}
}
