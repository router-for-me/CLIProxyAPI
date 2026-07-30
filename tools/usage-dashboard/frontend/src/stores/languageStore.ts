import { create } from "zustand";
import { persist } from "zustand/middleware";

export type Lang = "zh" | "en";

type Dict = Record<string, { zh: string; en: string }>;

export const DICT: Dict = {
  // Header / nav
  "app.title": { zh: "CLIProxyAPI 用量统计", en: "CLIProxyAPI Usage" },
  "nav.overview": { zh: "概览", en: "Overview" },
  "nav.usage": { zh: "用量明细", en: "Usage" },
  "action.refresh": { zh: "刷新", en: "Refresh" },
  "action.refresh_data": { zh: "刷新数据", en: "Refresh data" },
  // Range presets
  "range.1h": { zh: "近 1 小时", en: "Last 1h" },
  "range.5h": { zh: "近 5 小时", en: "Last 5h" },
  "range.24h": { zh: "近 24 小时", en: "Last 24h" },
  "range.7d": { zh: "近 7 天", en: "Last 7d" },
  "range.30d": { zh: "近 30 天", en: "Last 30d" },
  "range.custom": { zh: "自定义…", en: "Custom…" },
  "range.start": { zh: "起始时间", en: "Start" },
  "range.end": { zh: "结束时间", en: "End" },
  "range.apply": { zh: "应用", en: "Apply" },
  "range.custom_range": { zh: "自定义时间范围", en: "Custom Range" },
  "range.invalid": { zh: "起始时间需早于结束时间", en: "Start must be earlier than end" },
  // Health
  "health.healthy": { zh: "健康", en: "healthy" },
  "health.degraded": { zh: "降级", en: "degraded" },
  "health.unknown": { zh: "—", en: "—" },
  // KPI labels (Dashboard)
  "kpi.api_keys": { zh: "API 密钥", en: "API Keys" },
  "kpi.accounts": { zh: "账号数", en: "Accounts" },
  "kpi.today_requests": { zh: "今日请求", en: "Today Requests" },
  "kpi.active_keys": { zh: "活跃密钥", en: "Active Keys" },
  "kpi.today_tokens": { zh: "今日 Token", en: "Today Tokens" },
  "kpi.total_tokens": { zh: "总 Token", en: "Total Tokens" },
  "kpi.performance": { zh: "成功率", en: "Performance" },
  "kpi.avg_response": { zh: "平均响应", en: "Avg Response" },
  // KPI labels (Usage)
  "kpi.total_requests": { zh: "总请求数", en: "Total Requests" },
  "kpi.total_cost": { zh: "总费用", en: "Total Cost" },
  "kpi.avg_duration": { zh: "平均耗时", en: "Avg Duration" },
  "kpi.failed_suffix": { zh: "失败", en: "failed" },
  "kpi.partial_pricing": { zh: "部分模型无定价", en: "Some models unpriced" },
  // Tables - common
  "col.time": { zh: "时间", en: "Time" },
  "col.account": { zh: "账号", en: "Account" },
  "col.user": { zh: "用户", en: "User" },
  "col.model": { zh: "模型", en: "Model" },
  "col.provider": { zh: "供应商", en: "Provider" },
  "col.endpoint": { zh: "端点", en: "Endpoint" },
  "col.token": { zh: "Token", en: "Token" },
  "col.tokens": { zh: "Tokens", en: "Tokens" },
  "col.input": { zh: "输入", en: "Input" },
  "col.output": { zh: "输出", en: "Output" },
  "col.cost": { zh: "费用", en: "Cost" },
  "col.status": { zh: "状态", en: "Status" },
  "col.latency": { zh: "延迟", en: "Latency" },
  "col.duration": { zh: "耗时", en: "Duration" },
  "col.requests": { zh: "请求数", en: "Requests" },
  "col.status_code": { zh: "状态码", en: "Status Code" },
  "col.count": { zh: "数量", en: "Count" },
  "col.percentage": { zh: "百分比", en: "Percentage" },
  "col.last_seen": { zh: "最近出现", en: "Last Seen" },
  // Status badges
  "status.success": { zh: "成功", en: "Success" },
  "status.failed": { zh: "失败", en: "Failed" },
  // Filter bar
  "filter.model": { zh: "模型", en: "Model" },
  "filter.account_label": { zh: "账号", en: "Account" },
  "filter.provider": { zh: "供应商", en: "Provider" },
  "filter.endpoint": { zh: "端点", en: "Endpoint" },
  "action.reset": { zh: "重置", en: "Reset" },
  "action.column_settings": { zh: "列设置", en: "Column Settings" },
  "action.export_csv": { zh: "导出 CSV", en: "Export CSV" },
  "action.exporting": { zh: "导出中…", en: "Exporting…" },
  // Tabs
  "tab.usage": { zh: "用量", en: "Usage" },
  "tab.errors": { zh: "错误", en: "Errors" },
  "tab.ranking": { zh: "排行", en: "Ranking" },
  // Chart titles
  "chart.model_distribution": { zh: "模型分布", en: "Model Distribution" },
  "chart.provider_distribution": { zh: "供应商分布", en: "Provider Distribution" },
  "chart.endpoint_distribution": { zh: "端点分布", en: "Endpoint Distribution" },
  "chart.token_trend": { zh: "Token 趋势", en: "Token Trend" },
  "chart.token_usage_trend": { zh: "Token 用量趋势", en: "Token Usage Trend" },
  "chart.account_ranking": { zh: "账号排行", en: "Account Ranking" },
  // Chart mode toggle
  "chart.token_mode": { zh: "Token", en: "Token" },
  "chart.cost_mode": { zh: "费用", en: "Cost" },
  // States
  "state.loading": { zh: "加载中…", en: "Loading…" },
  "state.load_failed": { zh: "加载失败", en: "Load failed" },
  "state.retry": { zh: "重试", en: "Retry" },
  "state.no_data": { zh: "暂无数据", en: "No data" },
  "state.showing_n_results": { zh: "显示 {n} 条结果", en: "Showing {n} results" },
  // Detail drawer
  "drawer.request_detail": { zh: "请求详情", en: "Request Detail" },
};

export function translate(key: string, lang: Lang, vars?: Record<string, string | number>): string {
  const entry = DICT[key];
  if (!entry) return key;
  let s = entry[lang] ?? entry.zh;
  if (vars) {
    for (const [k, v] of Object.entries(vars)) {
      s = s.replace(`{${k}}`, String(v));
    }
  }
  return s;
}

interface LanguageState {
  lang: Lang;
  setLang: (l: Lang) => void;
}

export const useLanguageStore = create<LanguageState>()(
  persist(
    (set) => ({
      lang: "zh",
      setLang: (lang) => set({ lang }),
    }),
    { name: "usage-dashboard-lang" },
  ),
);

/** Translate hook — re-renders the component on language change. */
export function useT() {
  const lang = useLanguageStore((s) => s.lang);
  return (key: string, vars?: Record<string, string | number>) => translate(key, lang, vars);
}
