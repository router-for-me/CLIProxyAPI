import { isRecord } from '@/utils/helpers';
import { PROVIDER_BRAND_ORDER } from './descriptors';
import {
  PROVIDER_SORT_BY_VALUES,
  SORT_DIR_VALUES,
  type ProviderBrand,
  type ProviderSortBy,
  type SortDir,
} from './types';

const PROVIDERS_UI_STATE_KEY = 'providersPage.uiState';
const DEFAULT_ACTIVE_BRAND: ProviderBrand = 'gemini';
const DEFAULT_PROVIDER_FILTER_STATE: ProviderFilterState = {
  filter: '',
  sortBy: 'name',
  sortDir: 'asc',
  selectedModels: [],
};

const PROVIDER_BRAND_SET = new Set<ProviderBrand>(PROVIDER_BRAND_ORDER);
const PROVIDER_SORT_BY_SET = new Set<ProviderSortBy>(PROVIDER_SORT_BY_VALUES);
const SORT_DIR_SET = new Set<SortDir>(SORT_DIR_VALUES);

export interface ProviderFilterState {
  filter: string;
  sortBy: ProviderSortBy;
  sortDir: SortDir;
  selectedModels: string[];
}

export interface ProvidersWorkbenchUiState {
  activeBrand: ProviderBrand;
  filtersByBrand: Partial<Record<ProviderBrand, ProviderFilterState>>;
}

const isProviderBrand = (value: unknown): value is ProviderBrand =>
  typeof value === 'string' && PROVIDER_BRAND_SET.has(value as ProviderBrand);

const isProviderSortBy = (value: unknown): value is ProviderSortBy =>
  typeof value === 'string' && PROVIDER_SORT_BY_SET.has(value as ProviderSortBy);

const isSortDir = (value: unknown): value is SortDir =>
  typeof value === 'string' && SORT_DIR_SET.has(value as SortDir);

const normalizeSelectedModels = (value: unknown): string[] => {
  if (!Array.isArray(value)) return [];
  const seen = new Set<string>();
  value.forEach((item) => {
    if (typeof item !== 'string') return;
    const name = item.trim();
    if (!name) return;
    seen.add(name);
  });
  return Array.from(seen);
};

const normalizeProviderFilterState = (value: unknown): ProviderFilterState => {
  if (!isRecord(value)) return { ...DEFAULT_PROVIDER_FILTER_STATE };
  return {
    filter: typeof value.filter === 'string' ? value.filter : '',
    sortBy: isProviderSortBy(value.sortBy) ? value.sortBy : 'name',
    sortDir: isSortDir(value.sortDir) ? value.sortDir : 'asc',
    selectedModels: normalizeSelectedModels(value.selectedModels),
  };
};

const createDefaultProvidersWorkbenchUiState = (): ProvidersWorkbenchUiState => ({
  activeBrand: DEFAULT_ACTIVE_BRAND,
  filtersByBrand: {},
});

export const getProviderFilterState = (
  state: ProvidersWorkbenchUiState,
  brand: ProviderBrand
): ProviderFilterState => state.filtersByBrand[brand] ?? DEFAULT_PROVIDER_FILTER_STATE;

export const readProvidersWorkbenchUiState = (): ProvidersWorkbenchUiState => {
  if (typeof window === 'undefined') return createDefaultProvidersWorkbenchUiState();

  try {
    const raw = window.localStorage.getItem(PROVIDERS_UI_STATE_KEY);
    if (!raw) return createDefaultProvidersWorkbenchUiState();

    const parsed = JSON.parse(raw);
    if (!isRecord(parsed)) return createDefaultProvidersWorkbenchUiState();

    const source = isRecord(parsed.filtersByBrand) ? parsed.filtersByBrand : {};
    const filtersByBrand: ProvidersWorkbenchUiState['filtersByBrand'] = {};
    PROVIDER_BRAND_ORDER.forEach((brand) => {
      const filterState = source[brand];
      if (filterState !== undefined) {
        filtersByBrand[brand] = normalizeProviderFilterState(filterState);
      }
    });

    return {
      activeBrand: isProviderBrand(parsed.activeBrand) ? parsed.activeBrand : DEFAULT_ACTIVE_BRAND,
      filtersByBrand,
    };
  } catch {
    return createDefaultProvidersWorkbenchUiState();
  }
};

export const writeProvidersWorkbenchUiState = (state: ProvidersWorkbenchUiState) => {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(PROVIDERS_UI_STATE_KEY, JSON.stringify(state));
  } catch {
    // ignore storage failures
  }
};
