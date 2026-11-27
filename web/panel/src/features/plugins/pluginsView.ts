export type PluginsView = 'installed' | 'store';

export const PLUGIN_VIEWS: readonly PluginsView[] = ['installed', 'store'];

/** The Store view's address; the old `/plugin-store` route redirects here. */
export const PLUGINS_STORE_PATH = '/plugins?view=store';

export const parsePluginsView = (value: string | null): PluginsView =>
  value === 'store' ? 'store' : 'installed';

export const pluginViewTabId = (view: PluginsView) => `plugins-view-tab-${view}`;
export const pluginViewPanelId = (view: PluginsView) => `plugins-view-panel-${view}`;

/** How the Store view arranges entries. The choice is remembered per browser. */
export type StoreLayout = 'list' | 'cards';

export const STORE_LAYOUTS: readonly StoreLayout[] = ['list', 'cards'];
export const STORE_LAYOUT_STORAGE_KEY = 'pluginsPage.storeLayout';

export const parseStoreLayout = (value: unknown): StoreLayout =>
  value === 'cards' ? 'cards' : 'list';

export const readStoreLayout = (): StoreLayout => {
  if (typeof window === 'undefined') return 'list';
  try {
    return parseStoreLayout(window.localStorage.getItem(STORE_LAYOUT_STORAGE_KEY));
  } catch {
    return 'list';
  }
};

export const writeStoreLayout = (layout: StoreLayout) => {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(STORE_LAYOUT_STORAGE_KEY, layout);
  } catch {
    // ignore
  }
};
