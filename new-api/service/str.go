package service

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"sync"

	goahocorasick "github.com/anknown/ahocorasick"
)

func SundaySearch(text string, pattern string) bool {
	// 计算偏移表
	offset := make(map[rune]int)
	for i, c := range pattern {
		offset[c] = len(pattern) - i
	}

	// 文本串长度和模式串长度
	n, m := len(text), len(pattern)

	// 主循环，i表示当前对齐的文本串位置
	for i := 0; i <= n-m; {
		// 检查子串
		j := 0
		for j < m && text[i+j] == pattern[j] {
			j++
		}
		// 如果完全匹配，返回匹配位置
		if j == m {
			return true
		}

		// 如果还有剩余字符，则检查下一位字符在偏移表中的值
		if i+m < n {
			next := rune(text[i+m])
			if val, ok := offset[next]; ok {
				i += val // 存在于偏移表中，进行跳跃
			} else {
				i += len(pattern) + 1 // 不存在于偏移表中，跳过整个模式串长度
			}
		} else {
			break
		}
	}
	return false // 如果没有找到匹配，返回-1
}

func RemoveDuplicate(s []string) []string {
	result := make([]string, 0, len(s))
	temp := map[string]struct{}{}
	for _, item := range s {
		if _, ok := temp[item]; !ok {
			temp[item] = struct{}{}
			result = append(result, item)
		}
	}
	return result
}

func InitAc(dict []string) *goahocorasick.Machine {
	m := new(goahocorasick.Machine)
	runes := readRunes(dict)
	if err := m.Build(runes); err != nil {
		fmt.Println(err)
		return nil
	}
	return m
}

var acCache sync.Map

func acKey(dict []string) string {
	if len(dict) == 0 {
		return ""
	}
	normalized := make([]string, 0, len(dict))
	for _, w := range dict {
		w = strings.ToLower(strings.TrimSpace(w))
		if w != "" {
			normalized = append(normalized, w)
		}
	}
	if len(normalized) == 0 {
		return ""
	}
	sort.Strings(normalized)
	hasher := fnv.New64a()
	for _, w := range normalized {
		hasher.Write([]byte{0})
		hasher.Write([]byte(w))
	}
	return fmt.Sprintf("%x", hasher.Sum64())
}

func getOrBuildAC(dict []string) *goahocorasick.Machine {
	key := acKey(dict)
	if key == "" {
		return nil
	}
	if v, ok := acCache.Load(key); ok {
		if m, ok2 := v.(*goahocorasick.Machine); ok2 {
			return m
		}
	}
	m := InitAc(dict)
	if m == nil {
		return nil
	}
	if actual, loaded := acCache.LoadOrStore(key, m); loaded {
		if cached, ok := actual.(*goahocorasick.Machine); ok {
			return cached
		}
	}
	return m
}

func readRunes(dict []string) [][]rune {
	var runes [][]rune

	for _, word := range dict {
		word = strings.ToLower(word)
		l := bytes.TrimSpace([]byte(word))
		runes = append(runes, bytes.Runes(l))
	}

	return runes
}

func AcSearch(findText string, dict []string, stopImmediately bool) (bool, []string) {
	if len(dict) == 0 {
		return false, nil
	}
	if len(findText) == 0 {
		return false, nil
	}
	m := getOrBuildAC(dict)
	if m == nil {
		return false, nil
	}
	hits := m.MultiPatternSearch([]rune(findText), stopImmediately)
	if len(hits) > 0 {
		words := make([]string, 0)
		for _, hit := range hits {
			words = append(words, string(hit.Word))
		}
		return true, words
	}
	return false, nil
}
