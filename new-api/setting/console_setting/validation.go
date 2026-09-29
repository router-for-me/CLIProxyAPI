package console_setting

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/QuantumNous/new-api/common"
)

var (
	urlRegex       = regexp.MustCompile(`^https?://(?:(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)*[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?|(?:(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(?:25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?))(?:\:[0-9]{1,5})?(?:/.*)?$`)
	dangerousChars = []string{"<script", "<iframe", "javascript:", "onload=", "onerror=", "onclick="}
	validColors    = map[string]bool{
		"blue": true, "green": true, "cyan": true, "purple": true, "pink": true,
		"red": true, "orange": true, "amber": true, "yellow": true, "lime": true,
		"light-green": true, "teal": true, "light-blue": true, "indigo": true,
		"violet": true, "grey": true, "slate": true,
	}
	slugRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
)

func parseJSONArray(jsonStr string, typeName string) ([]map[string]interface{}, error) {
	var list []map[string]interface{}
	if err := common.UnmarshalJsonStr(jsonStr, &list); err != nil {
		return nil, fmt.Errorf("%s格式错误：%s", typeName, err.Error())
	}
	return list, nil
}

func exceedsMaxCharacters(s string, max int) bool {
	return len(utf16.Encode([]rune(s))) > max
}

func validateURL(urlStr string, index int, itemType string) error {
	if !urlRegex.MatchString(urlStr) {
		return fmt.Errorf("第%d个%s的URL格式不正确", index, itemType)
	}
	if _, err := url.Parse(urlStr); err != nil {
		return fmt.Errorf("第%d个%s的URL无法解析：%s", index, itemType, err.Error())
	}
	return nil
}

func checkDangerousContent(content string, index int, itemType string) error {
	lower := strings.ToLower(content)
	for _, d := range dangerousChars {
		if strings.Contains(lower, d) {
			return fmt.Errorf("第%d个%s包含不允许的内容", index, itemType)
		}
	}
	return nil
}

func getJSONList(jsonStr string) []map[string]interface{} {
	if jsonStr == "" {
		return []map[string]interface{}{}
	}
	var list []map[string]interface{}
	_ = common.UnmarshalJsonStr(jsonStr, &list)
	return list
}

func ValidateConsoleSettings(settingsStr string, settingType string) error {
	if settingsStr == "" {
		return nil
	}

	switch settingType {
	case "ApiInfo":
		return validateApiInfo(settingsStr)
	case "Announcements":
		return validateAnnouncements(settingsStr)
	case "FAQ":
		return validateFAQ(settingsStr)
	case "UptimeKumaGroups":
		return validateUptimeKumaGroups(settingsStr)
	default:
		return fmt.Errorf("未知的设置类型：%s", settingType)
	}
}

func validateApiInfo(apiInfoStr string) error {
	apiInfoList, err := parseJSONArray(apiInfoStr, "API信息")
	if err != nil {
		return err
	}

	if len(apiInfoList) > 50 {
		return fmt.Errorf("API信息数量不能超过50个")
	}

	for i, apiInfo := range apiInfoList {
		urlStr, ok := apiInfo["url"].(string)
		if !ok || urlStr == "" {
			return fmt.Errorf("第%d个API信息缺少URL字段", i+1)
		}
		route, ok := apiInfo["route"].(string)
		if !ok || route == "" {
			return fmt.Errorf("第%d个API信息缺少线路描述字段", i+1)
		}
		description, ok := apiInfo["description"].(string)
		if !ok || description == "" {
			return fmt.Errorf("第%d个API信息缺少说明字段", i+1)
		}
		color, ok := apiInfo["color"].(string)
		if !ok || color == "" {
			return fmt.Errorf("第%d个API信息缺少颜色字段", i+1)
		}

		if err := validateURL(urlStr, i+1, "API信息"); err != nil {
			return err
		}

		if exceedsMaxCharacters(urlStr, 500) {
			return fmt.Errorf("第%d个API信息的URL长度不能超过500字符", i+1)
		}
		if exceedsMaxCharacters(route, 100) {
			return fmt.Errorf("第%d个API信息的线路描述长度不能超过100字符", i+1)
		}
		if exceedsMaxCharacters(description, 200) {
			return fmt.Errorf("第%d个API信息的说明长度不能超过200字符", i+1)
		}

		if !validColors[color] {
			return fmt.Errorf("第%d个API信息的颜色值不合法", i+1)
		}

		if err := checkDangerousContent(description, i+1, "API信息"); err != nil {
			return err
		}
		if err := checkDangerousContent(route, i+1, "API信息"); err != nil {
			return err
		}
	}
	return nil
}

func GetApiInfo() []map[string]interface{} {
	return getJSONList(GetConsoleSetting().ApiInfo)
}

func validateAnnouncements(announcementsStr string) error {
	list, err := parseJSONArray(announcementsStr, "系统公告")
	if err != nil {
		return err
	}
	if len(list) > 100 {
		return fmt.Errorf("系统公告数量不能超过100个")
	}
	validTypes := map[string]bool{
		"default": true, "ongoing": true, "success": true, "warning": true, "error": true,
	}
	for i, ann := range list {
		content, ok := ann["content"].(string)
		if !ok || content == "" {
			return fmt.Errorf("第%d个公告缺少内容字段", i+1)
		}
		publishDateAny, exists := ann["publishDate"]
		if !exists {
			return fmt.Errorf("第%d个公告缺少发布日期字段", i+1)
		}
		publishDateStr, ok := publishDateAny.(string)
		if !ok || publishDateStr == "" {
			return fmt.Errorf("第%d个公告的发布日期不能为空", i+1)
		}
		if _, err := time.Parse(time.RFC3339, publishDateStr); err != nil {
			return fmt.Errorf("第%d个公告的发布日期格式错误", i+1)
		}
		if t, exists := ann["type"]; exists {
			if typeStr, ok := t.(string); ok {
				if !validTypes[typeStr] {
					return fmt.Errorf("第%d个公告的类型值不合法", i+1)
				}
			}
		}
		if exceedsMaxCharacters(content, 500) {
			return fmt.Errorf("第%d个公告的内容长度不能超过500字符", i+1)
		}
		if extra, exists := ann["extra"]; exists {
			if extraStr, ok := extra.(string); ok && exceedsMaxCharacters(extraStr, 100) {
				return fmt.Errorf("第%d个公告的说明长度不能超过100字符", i+1)
			}
		}
	}
	return nil
}

func validateFAQ(faqStr string) error {
	list, err := parseJSONArray(faqStr, "FAQ信息")
	if err != nil {
		return err
	}
	if len(list) > 100 {
		return fmt.Errorf("FAQ数量不能超过100个")
	}
	for i, faq := range list {
		question, ok := faq["question"].(string)
		if !ok || question == "" {
			return fmt.Errorf("第%d个FAQ缺少问题字段", i+1)
		}
		answer, ok := faq["answer"].(string)
		if !ok || answer == "" {
			return fmt.Errorf("第%d个FAQ缺少答案字段", i+1)
		}
		if exceedsMaxCharacters(question, 200) {
			return fmt.Errorf("第%d个FAQ的问题长度不能超过200字符", i+1)
		}
		if exceedsMaxCharacters(answer, 1000) {
			return fmt.Errorf("第%d个FAQ的答案长度不能超过1000字符", i+1)
		}
	}
	return nil
}

func getPublishTime(item map[string]interface{}) time.Time {
	if v, ok := item["publishDate"]; ok {
		if s, ok2 := v.(string); ok2 {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

func GetAnnouncements() []map[string]interface{} {
	list := getJSONList(GetConsoleSetting().Announcements)
	sort.SliceStable(list, func(i, j int) bool {
		return getPublishTime(list[i]).After(getPublishTime(list[j]))
	})
	return list
}

func GetFAQ() []map[string]interface{} {
	return getJSONList(GetConsoleSetting().FAQ)
}

func validateUptimeKumaGroups(groupsStr string) error {
	groups, err := parseJSONArray(groupsStr, "Uptime Kuma分组配置")
	if err != nil {
		return err
	}

	if len(groups) > 20 {
		return fmt.Errorf("Uptime Kuma分组数量不能超过20个")
	}

	nameSet := make(map[string]bool)

	for i, group := range groups {
		categoryName, ok := group["categoryName"].(string)
		if !ok || categoryName == "" {
			return fmt.Errorf("第%d个分组缺少分类名称字段", i+1)
		}
		if nameSet[categoryName] {
			return fmt.Errorf("第%d个分组的分类名称与其他分组重复", i+1)
		}
		nameSet[categoryName] = true
		urlStr, ok := group["url"].(string)
		if !ok || urlStr == "" {
			return fmt.Errorf("第%d个分组缺少URL字段", i+1)
		}
		slug, ok := group["slug"].(string)
		if !ok || slug == "" {
			return fmt.Errorf("第%d个分组缺少Slug字段", i+1)
		}
		description, ok := group["description"].(string)
		if !ok {
			description = ""
		}

		if err := validateURL(urlStr, i+1, "分组"); err != nil {
			return err
		}

		if exceedsMaxCharacters(categoryName, 50) {
			return fmt.Errorf("第%d个分组的分类名称长度不能超过50字符", i+1)
		}
		if exceedsMaxCharacters(urlStr, 500) {
			return fmt.Errorf("第%d个分组的URL长度不能超过500字符", i+1)
		}
		if exceedsMaxCharacters(slug, 100) {
			return fmt.Errorf("第%d个分组的Slug长度不能超过100字符", i+1)
		}
		if exceedsMaxCharacters(description, 200) {
			return fmt.Errorf("第%d个分组的描述长度不能超过200字符", i+1)
		}

		if !slugRegex.MatchString(slug) {
			return fmt.Errorf("第%d个分组的Slug只能包含字母、数字、下划线和连字符", i+1)
		}

		if err := checkDangerousContent(description, i+1, "分组"); err != nil {
			return err
		}
		if err := checkDangerousContent(categoryName, i+1, "分组"); err != nil {
			return err
		}
	}
	return nil
}

func GetUptimeKumaGroups() []map[string]interface{} {
	return getJSONList(GetConsoleSetting().UptimeKumaGroups)
}
