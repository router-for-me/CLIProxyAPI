import { useCallback, useState } from 'react';
import { useLocation, useSearchParams } from 'react-router-dom';
import { parsePluginsView, type PluginsView } from '../pluginsView';

/**
 * The active view, read once from `?view=` and mirrored back with `replace`.
 *
 * Local state is the source of truth: PageTransition freezes a layer's location while the
 * pathname is unchanged, so `useSearchParams` here keeps returning the entry-time search. The
 * mirror uses `replace` so switching views never adds history entries; reload and links still
 * land on the right view.
 */
export function usePluginsView() {
  const location = useLocation();
  const [searchParams, setSearchParams] = useSearchParams();
  const [view, setViewState] = useState<PluginsView>(() =>
    parsePluginsView(searchParams.get('view'))
  );

  const setView = useCallback(
    (next: PluginsView) => {
      setViewState(next);
      const params = new URLSearchParams();
      if (next === 'store') params.set('view', 'store');
      setSearchParams(params, { replace: true, state: location.state });
    },
    [location.state, setSearchParams]
  );

  return [view, setView] as const;
}
