package tui

// i18n provides a simple internationalization system for the TUI.
// Supported locales: "zh" (Chinese), "en" (English), "fa" (Farsi).

var currentLocale = "en"

// SetLocale changes the active locale.
func SetLocale(locale string) {
	if _, ok := locales[locale]; ok {
		currentLocale = locale
	}
}

// CurrentLocale returns the active locale code.
func CurrentLocale() string {
	return currentLocale
}

// ToggleLocale rotates through en -> zh -> fa.
func ToggleLocale() {
	switch currentLocale {
	case "en":
		currentLocale = "zh"
	case "zh":
		currentLocale = "fa"
	default:
		currentLocale = "en"
	}
}

// T returns the translated string for the given key.
func T(key string) string {
	if m, ok := locales[currentLocale]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	// Fallback to English
	if m, ok := locales["en"]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return key
}

var locales = map[string]map[string]string{
	"zh": zhStrings,
	"en": enStrings,
	"fa": faStrings,
}

// ──────────────────────────────────────────
// Tab names
// ──────────────────────────────────────────
var zhTabNames = []string{"仪表盘", "配置", "认证文件", "API 密钥", "OAuth", "使用统计", "日志"}
var enTabNames = []string{"Dashboard", "Config", "Auth Files", "API Keys", "OAuth", "Usage", "Logs"}
var faTabNames = []string{"داشبورد", "پیکربندی", "فایل\u200cهای احراز هویت", "کلیدهای API", "OAuth", "کاربرد", "لاگ\u200cها"}

// TabNames returns tab names in the current locale.
func TabNames() []string {
	switch currentLocale {
	case "zh":
		return zhTabNames
	case "fa":
		return faTabNames
	default:
		return enTabNames
	}
}

var zhStrings = map[string]string{
	// ── Common ──
	"loading":      "加载中...",
	"refresh":      "刷新",
	"save":         "保存",
	"cancel":       "取消",
	"confirm":      "确认",
	"yes":          "是",
	"no":           "否",
	"error":        "错误",
	"success":      "成功",
	"navigate":     "导航",
	"scroll":       "滚动",
	"enter_save":   "Enter: 保存",
	"esc_cancel":   "Esc: 取消",
	"enter_submit": "Enter: 提交",
	"press_r":      "[r] 刷新",
	"press_scroll": "[↑↓] 滚动",
	"not_set":      "(未设置)",
	"error_prefix": "⚠ 错误: ",

	// ── Status bar ──
	"status_left":                 " CLIProxyAPI 管理终端",
	"status_right":                "Tab/Shift+Tab: 切换 • L: 语言 • q/Ctrl+C: 退出 ",
	"initializing_tui":            "正在初始化...",
	"auth_gate_title":             "🔐 连接管理 API",
	"auth_gate_help":              " 请输入管理密码并按 Enter 连接",
	"auth_gate_password":          "密码",
	"auth_gate_enter":             " Enter: 连接 • q/Ctrl+C: 退出 • L: 语言",
	"auth_gate_connecting":        "正在连接...",
	"auth_gate_connect_fail":      "连接失败：%s",
	"auth_gate_password_required": "请输入密码",

	// ── Dashboard ──
	"dashboard_title":  "📊 仪表盘",
	"dashboard_help":   " [r] 刷新 • [↑↓] 滚动",
	"connected":        "● 已连接",
	"mgmt_keys":        "管理密钥",
	"auth_files_label": "认证文件",
	"active_suffix":    "活跃",
	"total_requests":   "请求",
	"success_label":    "成功",
	"failure_label":    "失败",
	"total_tokens":     "总 Tokens",
	"current_config":   "当前配置",
	"debug_mode":       "启用调试模式",
	"usage_stats":      "启用使用统计",
	"log_to_file":      "启用日志记录到文件",
	"retry_count":      "重试次数",
	"proxy_url":        "代理 URL",
	"routing_strategy": "路由策略",
	"model_stats":      "模型统计",
	"model":            "模型",
	"requests":         "请求数",
	"tokens":           "Tokens",
	"bool_yes":         "是 ✓",
	"bool_no":          "否",

	// ── Config ──
	"config_title":      "⚙ 配置",
	"config_help1":      "  [↑↓/jk] 导航 • [Enter/Space] 编辑 • [r] 刷新",
	"config_help2":      "  布尔: Enter 切换 • 文本/数字: Enter 输入, Enter 确认, Esc 取消",
	"updated_ok":        "✓ 更新成功",
	"no_config":         "  未加载配置",
	"invalid_int":       "无效整数",
	"section_server":    "服务器",
	"section_logging":   "日志与统计",
	"section_quota":     "配额超限处理",
	"section_routing":   "路由",
	"section_websocket": "WebSocket",
	"section_ampcode":   "AMP Code",
	"section_other":     "其他",

	// ── Auth Files ──
	"auth_title":      "🔑 认证文件",
	"auth_help1":      " [↑↓/jk] 导航 • [Enter] 展开 • [e] 启用/停用 • [d] 删除 • [r] 刷新",
	"auth_help2":      " [1] 编辑 prefix • [2] 编辑 proxy_url • [3] 编辑 priority",
	"no_auth_files":   "  无认证文件",
	"confirm_delete":  "⚠ 删除 %s? [y/n]",
	"deleted":         "已删除 %s",
	"enabled":         "已启用",
	"disabled":        "已停用",
	"updated_field":   "已更新 %s 的 %s",
	"status_active":   "活跃",
	"status_disabled": "已停用",

	// ── API Keys ──
	"keys_title":         "🔐 API 密钥",
	"keys_help":          " [↑↓/jk] 导航 • [a] 添加 • [e] 编辑 • [d] 删除 • [c] 复制 • [r] 刷新",
	"no_keys":            "  无 API Key，按 [a] 添加",
	"access_keys":        "Access API Keys",
	"confirm_delete_key": "⚠ 确认删除 %s? [y/n]",
	"key_added":          "已添加 API Key",
	"key_updated":        "已更新 API Key",
	"key_deleted":        "已删除 API Key",
	"copied":             "✓ 已复制到剪贴板",
	"copy_failed":        "✗ 复制失败",
	"new_key_prompt":     "  New Key: ",
	"edit_key_prompt":    "  Edit Key: ",
	"enter_add":          "    Enter: 添加 • Esc: 取消",
	"enter_save_esc":     "    Enter: 保存 • Esc: 取消",

	// ── OAuth ──
	"oauth_title":        "🔐 OAuth 登录",
	"oauth_select":       "  选择提供商并按 [Enter] 开始 OAuth 登录:",
	"oauth_help":         "  [↑↓/jk] 导航 • [Enter] 登录 • [Esc] 清除状态",
	"oauth_initiating":   "⏳ 正在初始化 %s 登录...",
	"oauth_success":      "认证成功! 请刷新 Auth Files 标签查看新凭证。",
	"oauth_completed":    "认证流程已完成。",
	"oauth_failed":       "认证失败",
	"oauth_timeout":      "OAuth 流程超时 (5 分钟)",
	"oauth_press_esc":    "  按 [Esc] 取消",
	"oauth_auth_url":     "  授权链接:",
	"oauth_remote_hint":  "  远程浏览器模式：在浏览器中打开上述链接完成授权后，将回调 URL 粘贴到下方。",
	"oauth_callback_url": "  回调 URL:",
	"oauth_press_c":      "  按 [c] 输入回调 URL • [Esc] 返回",
	"oauth_submitting":   "⏳ 提交回调中...",
	"oauth_submit_ok":    "✓ 回调已提交，等待处理...",
	"oauth_submit_fail":  "✗ 提交回调失败",
	"oauth_waiting":      "  等待认证中...",

	// ── Usage ──
	"usage_title":         "📈 使用统计",
	"usage_help":          " [r] 刷新 • [↑↓] 滚动",
	"usage_no_data":       "  使用数据不可用",
	"usage_total_reqs":    "总请求数",
	"usage_total_tokens":  "总 Token 数",
	"usage_success":       "成功",
	"usage_failure":       "失败",
	"usage_total_token_l": "总Token",
	"usage_rpm":           "RPM",
	"usage_tpm":           "TPM",
	"usage_req_by_hour":   "请求趋势 (按小时)",
	"usage_tok_by_hour":   "Token 使用趋势 (按小时)",
	"usage_req_by_day":    "请求趋势 (按天)",
	"usage_api_detail":    "API 详细统计",
	"usage_input":         "输入",
	"usage_output":        "输出",
	"usage_cached":        "缓存",
	"usage_reasoning":     "思考",

	// ── Logs ──
	"logs_title":       "📋 日志",
	"logs_auto_scroll": "● 自动滚动",
	"logs_paused":      "○ 已暂停",
	"logs_filter":      "过滤",
	"logs_lines":       "行数",
	"logs_help":        " [a] 自动滚动 • [c] 清除 • [1] 全部 [2] info+ [3] warn+ [4] error • [↑↓] 滚动",
	"logs_waiting":     "  等待日志输出...",
}

var enStrings = map[string]string{
	// ── Common ──
	"loading":      "Loading...",
	"refresh":      "Refresh",
	"save":         "Save",
	"cancel":       "Cancel",
	"confirm":      "Confirm",
	"yes":          "Yes",
	"no":           "No",
	"error":        "Error",
	"success":      "Success",
	"navigate":     "Navigate",
	"scroll":       "Scroll",
	"enter_save":   "Enter: Save",
	"esc_cancel":   "Esc: Cancel",
	"enter_submit": "Enter: Submit",
	"press_r":      "[r] Refresh",
	"press_scroll": "[↑↓] Scroll",
	"not_set":      "(not set)",
	"error_prefix": "⚠ Error: ",

	// ── Status bar ──
	"status_left":                 " CLIProxyAPI Management TUI",
	"status_right":                "Tab/Shift+Tab: switch • L: lang • q/Ctrl+C: quit ",
	"initializing_tui":            "Initializing...",
	"auth_gate_title":             "🔐 Connect Management API",
	"auth_gate_help":              " Enter management password and press Enter to connect",
	"auth_gate_password":          "Password",
	"auth_gate_enter":             " Enter: connect • q/Ctrl+C: quit • L: lang",
	"auth_gate_connecting":        "Connecting...",
	"auth_gate_connect_fail":      "Connection failed: %s",
	"auth_gate_password_required": "password is required",

	// ── Dashboard ──
	"dashboard_title":  "📊 Dashboard",
	"dashboard_help":   " [r] Refresh • [↑↓] Scroll",
	"connected":        "● Connected",
	"mgmt_keys":        "Mgmt Keys",
	"auth_files_label": "Auth Files",
	"active_suffix":    "active",
	"total_requests":   "Requests",
	"success_label":    "Success",
	"failure_label":    "Failed",
	"total_tokens":     "Total Tokens",
	"current_config":   "Current Config",
	"debug_mode":       "Debug Mode",
	"usage_stats":      "Usage Statistics",
	"log_to_file":      "Log to File",
	"retry_count":      "Retry Count",
	"proxy_url":        "Proxy URL",
	"routing_strategy": "Routing Strategy",
	"model_stats":      "Model Stats",
	"model":            "Model",
	"requests":         "Requests",
	"tokens":           "Tokens",
	"bool_yes":         "Yes ✓",
	"bool_no":          "No",

	// ── Config ──
	"config_title":      "⚙ Configuration",
	"config_help1":      "  [↑↓/jk] Navigate • [Enter/Space] Edit • [r] Refresh",
	"config_help2":      "  Bool: Enter to toggle • String/Int: Enter to type, Enter to confirm, Esc to cancel",
	"updated_ok":        "✓ Updated successfully",
	"no_config":         "  No configuration loaded",
	"invalid_int":       "invalid integer",
	"section_server":    "Server",
	"section_logging":   "Logging & Stats",
	"section_quota":     "Quota Exceeded Handling",
	"section_routing":   "Routing",
	"section_websocket": "WebSocket",
	"section_ampcode":   "AMP Code",
	"section_other":     "Other",

	// ── Auth Files ──
	"auth_title":      "🔑 Auth Files",
	"auth_help1":      " [↑↓/jk] Navigate • [Enter] Expand • [e] Enable/Disable • [d] Delete • [r] Refresh",
	"auth_help2":      " [1] Edit prefix • [2] Edit proxy_url • [3] Edit priority",
	"no_auth_files":   "  No auth files found",
	"confirm_delete":  "⚠ Delete %s? [y/n]",
	"deleted":         "Deleted %s",
	"enabled":         "Enabled",
	"disabled":        "Disabled",
	"updated_field":   "Updated %s on %s",
	"status_active":   "active",
	"status_disabled": "disabled",

	// ── API Keys ──
	"keys_title":         "🔐 API Keys",
	"keys_help":          " [↑↓/jk] Navigate • [a] Add • [e] Edit • [d] Delete • [c] Copy • [r] Refresh",
	"no_keys":            "  No API Keys. Press [a] to add",
	"access_keys":        "Access API Keys",
	"confirm_delete_key": "⚠ Delete %s? [y/n]",
	"key_added":          "API Key added",
	"key_updated":        "API Key updated",
	"key_deleted":        "API Key deleted",
	"copied":             "✓ Copied to clipboard",
	"copy_failed":        "✗ Copy failed",
	"new_key_prompt":     "  New Key: ",
	"edit_key_prompt":    "  Edit Key: ",
	"enter_add":          "    Enter: Add • Esc: Cancel",
	"enter_save_esc":     "    Enter: Save • Esc: Cancel",

	// ── OAuth ──
	"oauth_title":        "🔐 OAuth Login",
	"oauth_select":       "  Select a provider and press [Enter] to start OAuth login:",
	"oauth_help":         "  [↑↓/jk] Navigate • [Enter] Login • [Esc] Clear status",
	"oauth_initiating":   "⏳ Initiating %s login...",
	"oauth_success":      "Authentication successful! Refresh Auth Files tab to see the new credential.",
	"oauth_completed":    "Authentication flow completed.",
	"oauth_failed":       "Authentication failed",
	"oauth_timeout":      "OAuth flow timed out (5 minutes)",
	"oauth_press_esc":    "  Press [Esc] to cancel",
	"oauth_auth_url":     "  Authorization URL:",
	"oauth_remote_hint":  "  Remote browser mode: Open the URL above in browser, paste the callback URL below after authorization.",
	"oauth_callback_url": "  Callback URL:",
	"oauth_press_c":      "  Press [c] to enter callback URL • [Esc] to go back",
	"oauth_submitting":   "⏳ Submitting callback...",
	"oauth_submit_ok":    "✓ Callback submitted, waiting...",
	"oauth_submit_fail":  "✗ Callback submission failed",
	"oauth_waiting":      "  Waiting for authentication...",

	// ── Usage ──
	"usage_title":         "📈 Usage Statistics",
	"usage_help":          " [r] Refresh • [↑↓] Scroll",
	"usage_no_data":       "  Usage data not available",
	"usage_total_reqs":    "Total Requests",
	"usage_total_tokens":  "Total Tokens",
	"usage_success":       "Success",
	"usage_failure":       "Failed",
	"usage_total_token_l": "Total Tokens",
	"usage_rpm":           "RPM",
	"usage_tpm":           "TPM",
	"usage_req_by_hour":   "Requests by Hour",
	"usage_tok_by_hour":   "Token Usage by Hour",
	"usage_req_by_day":    "Requests by Day",
	"usage_api_detail":    "API Detail Statistics",
	"usage_input":         "Input",
	"usage_output":        "Output",
	"usage_cached":        "Cached",
	"usage_reasoning":     "Reasoning",

	// ── Logs ──
	"logs_title":       "📋 Logs",
	"logs_auto_scroll": "● AUTO-SCROLL",
	"logs_paused":      "○ PAUSED",
	"logs_filter":      "Filter",
	"logs_lines":       "Lines",
	"logs_help":        " [a] Auto-scroll • [c] Clear • [1] All [2] info+ [3] warn+ [4] error • [↑↓] Scroll",
	"logs_waiting":     "  Waiting for log output...",
}

var faStrings = map[string]string{
	// ── Common ──
	"loading":      "در حال بارگذاری...",
	"refresh":      "بازخوانی",
	"save":         "ذخیره",
	"cancel":       "لغو",
	"confirm":      "تایید",
	"yes":          "بله",
	"no":           "خیر",
	"error":        "خطا",
	"success":      "موفق",
	"navigate":     "جابجایی",
	"scroll":       "پیمایش",
	"enter_save":   "Enter: ذخیره",
	"esc_cancel":   "Esc: لغو",
	"enter_submit": "Enter: ارسال",
	"press_r":      "[r] بازخوانی",
	"press_scroll": "[↑↓] پیمایش",
	"not_set":      "(تنظیم نشده)",
	"error_prefix": "⚠ خطا: ",

	// ── Status bar ──
	"status_left":                 " CLIProxyAPI پنل مدیریت",
	"status_right":                "Tab/Shift+Tab: جابجایی • L: زبان • q/Ctrl+C: خروج ",
	"initializing_tui":            "در حال راه\u200cاندازی...",
	"auth_gate_title":             "🔐 اتصال به API مدیریت",
	"auth_gate_help":              " رمز عبور مدیریت را وارد کرده و Enter بزنید",
	"auth_gate_password":          "رمز عبور",
	"auth_gate_enter":             " Enter: اتصال • q/Ctrl+C: خروج • L: زبان",
	"auth_gate_connecting":        "در حال اتصال...",
	"auth_gate_connect_fail":      "اتصال ناموفق: %s",
	"auth_gate_password_required": "رمز عبور الزامی است",

	// ── Dashboard ──
	"dashboard_title":  "📊 داشبورد",
	"dashboard_help":   " [r] بازخوانی • [↑↓] پیمایش",
	"connected":        "● متصل",
	"mgmt_keys":        "کلیدهای مدیریت",
	"auth_files_label": "فایل\u200cهای احراز هویت",
	"active_suffix":    "فعال",
	"total_requests":   "درخواست\u200cها",
	"success_label":    "موفق",
	"failure_label":    "ناموفق",
	"total_tokens":     "مجموع توکن\u200cها",
	"current_config":   "پیکربندی فعلی",
	"debug_mode":       "حالت اشکال\u200cزدایی",
	"usage_stats":      "آمار مصرف",
	"log_to_file":      "ثبت لاگ در فایل",
	"retry_count":      "تعداد تلاش مجدد",
	"proxy_url":        "نشانی پروکسی",
	"routing_strategy": "استراتژی مسیریابی",
	"model_stats":      "آمار مدل\u200cها",
	"model":            "مدل",
	"requests":         "درخواست\u200cها",
	"tokens":           "توکن\u200cها",
	"bool_yes":         "بله ✓",
	"bool_no":          "خیر",

	// ── Config ──
	"config_title":      "⚙ پیکربندی",
	"config_help1":      "  [↑↓/jk] جابجایی • [Enter/Space] ویرایش • [r] بازخوانی",
	"config_help2":      "  بولی: Enter برای تغییر • متن/عدد: Enter برای ورود، Enter برای تایید، Esc برای لغو",
	"updated_ok":        "✓ با موفقیت به\u200cروزرسانی شد",
	"no_config":         "  پیکربندی بارگذاری نشده است",
	"invalid_int":       "عدد صحیح نامعتبر",
	"section_server":    "سرور",
	"section_logging":   "لاگ و آمار",
	"section_quota":     "مدیریت عبور از سهمیه",
	"section_routing":   "مسیریابی",
	"section_websocket": "وب\u200cسوکت",
	"section_ampcode":   "AMP Code",
	"section_other":     "سایر",

	// ── Auth Files ──
	"auth_title":      "🔑 فایل\u200cهای احراز هویت",
	"auth_help1":      " [↑↓/jk] جابجایی • [Enter] بازکردن • [e] فعال/غیرفعال • [d] حذف • [r] بازخوانی",
	"auth_help2":      " [1] ویرایش prefix • [2] ویرایش proxy_url • [3] ویرایش priority",
	"no_auth_files":   "  فایل احراز هویت یافت نشد",
	"confirm_delete":  "⚠ حذف %s؟ [y/n]",
	"deleted":         "%s حذف شد",
	"enabled":         "فعال شد",
	"disabled":        "غیرفعال شد",
	"updated_field":   "%s برای %s به\u200cروزرسانی شد",
	"status_active":   "فعال",
	"status_disabled": "غیرفعال",

	// ── API Keys ──
	"keys_title":         "🔐 کلیدهای API",
	"keys_help":          " [↑↓/jk] جابجایی • [a] افزودن • [e] ویرایش • [d] حذف • [c] کپی • [r] بازخوانی",
	"no_keys":            "  کلید API وجود ندارد. [a] را بزنید",
	"access_keys":        "کلیدهای دسترسی API",
	"confirm_delete_key": "⚠ حذف %s؟ [y/n]",
	"key_added":          "کلید API اضافه شد",
	"key_updated":        "کلید API به\u200cروزرسانی شد",
	"key_deleted":        "کلید API حذف شد",
	"copied":             "✓ در کلیپ\u200cبورد کپی شد",
	"copy_failed":        "✗ کپی ناموفق بود",
	"new_key_prompt":     "  کلید جدید: ",
	"edit_key_prompt":    "  ویرایش کلید: ",
	"enter_add":          "    Enter: افزودن • Esc: لغو",
	"enter_save_esc":     "    Enter: ذخیره • Esc: لغو",

	// ── OAuth ──
	"oauth_title":        "🔐 ورود OAuth",
	"oauth_select":       "  ارائه\u200cدهنده را انتخاب کرده و [Enter] را برای شروع بزنید:",
	"oauth_help":         "  [↑↓/jk] جابجایی • [Enter] ورود • [Esc] پاک\u200cکردن وضعیت",
	"oauth_initiating":   "⏳ شروع ورود %s...",
	"oauth_success":      "احراز هویت موفق بود! تب Auth Files را بازخوانی کنید.",
	"oauth_completed":    "فرایند احراز هویت کامل شد.",
	"oauth_failed":       "احراز هویت ناموفق بود",
	"oauth_timeout":      "مهلت OAuth تمام شد (5 دقیقه)",
	"oauth_press_esc":    "  [Esc] برای لغو",
	"oauth_auth_url":     "  نشانی مجوز:",
	"oauth_remote_hint":  "  حالت مرورگر راه\u200cدور: لینک بالا را باز کنید و بعد از احراز هویت، URL بازگشت را وارد کنید.",
	"oauth_callback_url": "  URL بازگشت:",
	"oauth_press_c":      "  [c] برای وارد کردن URL بازگشت • [Esc] برای بازگشت",
	"oauth_submitting":   "⏳ در حال ارسال بازگشت...",
	"oauth_submit_ok":    "✓ بازگشت ارسال شد، در انتظار پردازش...",
	"oauth_submit_fail":  "✗ ارسال بازگشت ناموفق بود",
	"oauth_waiting":      "  در انتظار احراز هویت...",

	// ── Usage ──
	"usage_title":         "📈 آمار مصرف",
	"usage_help":          " [r] بازخوانی • [↑↓] پیمایش",
	"usage_no_data":       "  داده مصرف موجود نیست",
	"usage_total_reqs":    "مجموع درخواست\u200cها",
	"usage_total_tokens":  "مجموع توکن\u200cها",
	"usage_success":       "موفق",
	"usage_failure":       "ناموفق",
	"usage_total_token_l": "مجموع توکن\u200cها",
	"usage_rpm":           "RPM",
	"usage_tpm":           "TPM",
	"usage_req_by_hour":   "درخواست\u200cها بر اساس ساعت",
	"usage_tok_by_hour":   "مصرف توکن بر اساس ساعت",
	"usage_req_by_day":    "درخواست\u200cها بر اساس روز",
	"usage_api_detail":    "آمار جزئی API",
	"usage_input":         "ورودی",
	"usage_output":        "خروجی",
	"usage_cached":        "کش\u200cشده",
	"usage_reasoning":     "استدلال",

	// ── Logs ──
	"logs_title":       "📋 لاگ\u200cها",
	"logs_auto_scroll": "● پیمایش خودکار",
	"logs_paused":      "○ متوقف",
	"logs_filter":      "فیلتر",
	"logs_lines":       "خطوط",
	"logs_help":        " [a] پیمایش خودکار • [c] پاکسازی • [1] همه [2] info+ [3] warn+ [4] error • [↑↓] پیمایش",
	"logs_waiting":     "  در انتظار خروجی لاگ...",
}
