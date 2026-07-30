import { useModels } from "@/api/hooks/useModels";
import { useAccounts } from "@/api/hooks/useAccounts";
import { useProviders } from "@/api/hooks/useProviders";
import { useEndpoints } from "@/api/hooks/useEndpoints";
import { useFiltersStore } from "@/stores/filtersStore";
import { useShallow } from "zustand/react/shallow";
import { useQueryClient } from "@tanstack/react-query";
import { FilterMultiSelect } from "./FilterMultiSelect";
import { Button } from "@/components/ui/button";
import { useT } from "@/stores/languageStore";

interface Props { onRefresh?: () => void; onExport?: () => void; onColumnSettings?: () => void; downloading?: boolean; }

export default function UsageFilterBar({ onRefresh, onExport, onColumnSettings, downloading }: Props) {
  const filterKey = useFiltersStore(useShallow((s) => ({ range: s.range, from: s.from, to: s.to })));
  const t = useT();
  const { data: modelData } = useModels(filterKey);
  const { data: accountData } = useAccounts(filterKey);
  const { data: providerData } = useProviders(filterKey);
  const { data: endpointData } = useEndpoints(filterKey);

  const selectedModels = useFiltersStore((s) => s.selectedModels);
  const selectedAccounts = useFiltersStore((s) => s.selectedAccounts);
  const selectedProviders = useFiltersStore((s) => s.selectedProviders);
  const selectedEndpoints = useFiltersStore((s) => s.selectedEndpoints);
  const toggleModel = useFiltersStore((s) => s.toggleModel);
  const toggleAccount = useFiltersStore((s) => s.toggleAccount);
  const toggleProvider = useFiltersStore((s) => s.toggleProvider);
  const toggleEndpoint = useFiltersStore((s) => s.toggleEndpoint);
  const clearAllFilters = useFiltersStore((s) => s.clearAllFilters);

  const qc = useQueryClient();

  return (
    <div className="flex flex-wrap items-center gap-2">
      <FilterMultiSelect label={t("filter.model")}
                         options={modelData?.models?.map((m) => ({ label: m.model, value: m.model })) ?? []}
                         selected={selectedModels} onToggle={toggleModel} />
      <FilterMultiSelect label={t("filter.account_label")}
                         options={accountData?.accounts?.map((a) => ({ label: a.account, value: a.account })) ?? []}
                         selected={selectedAccounts} onToggle={toggleAccount} />
      <FilterMultiSelect label={t("filter.provider")}
                         options={providerData?.providers?.map((p) => ({ label: p.provider, value: p.provider })) ?? []}
                         selected={selectedProviders} onToggle={toggleProvider} />
      <FilterMultiSelect label={t("filter.endpoint")}
                         options={endpointData?.endpoints?.map((e) => ({ label: e.endpoint, value: e.endpoint })) ?? []}
                         selected={selectedEndpoints} onToggle={toggleEndpoint} />
      <Button size="sm" onClick={() => { onRefresh?.(); qc.invalidateQueries(); }}>{t("action.refresh")}</Button>
      <Button size="sm" variant="ghost" onClick={() => { clearAllFilters(); }}>{t("action.reset")}</Button>
      {onColumnSettings && <Button size="sm" variant="ghost" onClick={onColumnSettings}>{t("action.column_settings")}</Button>}
      {onExport && <Button size="sm" disabled={downloading} onClick={onExport}>{downloading ? t("action.exporting") : t("action.export_csv")}</Button>}
    </div>
  );
}