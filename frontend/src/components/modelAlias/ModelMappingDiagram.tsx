import { forwardRef, useCallback, useEffect, useImperativeHandle, useLayoutEffect, useMemo, useRef, useState, type DragEvent, type MouseEvent as ReactMouseEvent } from 'react';
import { useTranslation } from 'react-i18next';
import type { OAuthModelAliasEntry } from '@/types';
import { useThemeStore } from '@/stores';
import { AliasColumn, ProviderColumn, SourceColumn } from './ModelMappingDiagramColumns';
import { DiagramContextMenu } from './ModelMappingDiagramContextMenu';
import {
  AddAliasModal,
  RenameAliasModal,
  SettingsAliasModal,
  SettingsSourceModal
} from './ModelMappingDiagramModals';
import type {
  AliasNode,
  AuthFileModelItem,
  ContextMenuState,
  DiagramLine,
  SourceNode
} from './ModelMappingDiagramTypes';
import styles from './ModelMappingDiagram.module.scss';

export interface ModelMappingDiagramProps {
  modelAlias: Record<string, OAuthModelAliasEntry[]>;
  allProviderModels?: Record<string, AuthFileModelItem[]>;
  onUpdate?: (provider: string, sourceModel: string, newAlias: string) => void;
  onDeleteLink?: (provider: string, sourceModel: string, alias: string) => void;
  onToggleFork?: (provider: string, sourceModel: string, alias: string, fork: boolean) => void;
  onRenameAlias?: (oldAlias: string, newAlias: string) => void;
  onDeleteAlias?: (alias: string) => void;
  onEditProvider?: (provider: string) => void;
  onDeleteProvider?: (provider: string) => void;
  className?: string;
}

const PROVIDER_COLORS = [
  '#8b8680', '#10b981', '#f59e0b', '#c65746',
  '#8b5cf6', '#ec4899', '#06b6d4', '#84cc16'
];

function getProviderColor(provider: string): string {
  const hash = provider.split('').reduce((acc, c) => acc + c.charCodeAt(0), 0);
  return PROVIDER_COLORS[hash % PROVIDER_COLORS.length];
}

export interface ModelMappingDiagramRef {
  collapseAll: () => void;
  refreshLayout: () => void;
}

export const ModelMappingDiagram = forwardRef<ModelMappingDiagramRef, ModelMappingDiagramProps>(function ModelMappingDiagram({ 
  modelAlias, 
  allProviderModels = {}, 
  onUpdate,
  onDeleteLink,
  onToggleFork,
  onRenameAlias,
  onDeleteAlias,
  onEditProvider,
  onDeleteProvider,
  className
}, ref) {
  const { t } = useTranslation();
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const isDark = resolvedTheme === 'dark';
  const enableTapLinking = useMemo(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia === 'undefined') return false;
    return (
      window.matchMedia('(any-pointer: coarse)').matches &&
      !window.matchMedia('(any-pointer: fine)').matches
    );
  }, []);
  
  const containerRef = useRef<HTMLDivElement>(null);
  const [lines, setLines] = useState<DiagramLine[]>([]);
  const [draggedSource, setDraggedSource] = useState<SourceNode | null>(null);
  const [draggedAlias, setDraggedAlias] = useState<string | null>(null);
  const [dropTargetAlias, setDropTargetAlias] = useState<string | null>(null);
  const [dropTargetSource, setDropTargetSource] = useState<string | null>(null);
  const [tapSourceId, setTapSourceId] = useState<string | null>(null);
  const [tapAlias, setTapAlias] = useState<string | null>(null);
  const [extraAliases, setExtraAliases] = useState<string[]>([]);
  const [contextMenu, setContextMenu] = useState<ContextMenuState | null>(null);
  const [collapsedProviders, setCollapsedProviders] = useState<Set<string>>(new Set());
  const [providerGroupHeights, setProviderGroupHeights] = useState<Record<string, number>>({});
  const [renameState, setRenameState] = useState<{ oldAlias: string } | null>(null);
  const [renameValue, setRenameValue] = useState('');
  const [renameError, setRenameError] = useState('');
  const [addAliasOpen, setAddAliasOpen] = useState(false);
  const [addAliasValue, setAddAliasValue] = useState('');
  const [addAliasError, setAddAliasError] = useState('');
  const [settingsAlias, setSettingsAlias] = useState<string | null>(null);
  const [settingsSourceId, setSettingsSourceId] = useState<string | null>(null);

  // Parse data: each source model (provider+name) and each alias is distinct by id; 1 source -> many aliases.
  const { aliasNodes, providerNodes } = useMemo(() => {
    const sourceMap = new Map<
      string,
      { provider: string; name: string; aliases: Map<string, boolean> }
    >();
    const aliasSet = new Set<string>();

    // 1. Existing mappings: group by (provider, name), each source has a set of aliases
    Object.entries(modelAlias).forEach(([provider, mappings]) => {
      (mappings ?? []).forEach((m) => {
        const name = (m?.name || '').trim();
        const alias = (m?.alias || '').trim();
        if (!name || !alias) return;

        const pk = `${provider.toLowerCase()}::${name.toLowerCase()}`;
        if (!sourceMap.has(pk)) {
          sourceMap.set(pk, { provider, name, aliases: new Map() });
        }
        sourceMap.get(pk)!.aliases.set(alias, m?.fork === true);
        aliasSet.add(alias);
      });
    });

    // 2. Unmapped models from allProviderModels (no mapping yet)
    Object.entries(allProviderModels).forEach(([provider, models]) => {
      (models ?? []).forEach((m) => {
        const name = (m.id || '').trim();
        if (!name) return;
        const pk = `${provider.toLowerCase()}::${name.toLowerCase()}`;
        if (sourceMap.has(pk)) {
          // Already in sourceMap from mappings; keep provider from mapping for correct grouping.
          return;
        }
        sourceMap.set(pk, { provider, name, aliases: new Map() });
      });
    });

    // 3. Source nodes: distinct by id = provider::name
    const sources: SourceNode[] = Array.from(sourceMap.entries())
      .map(([id, v]) => ({
        id,
        provider: v.provider,
        name: v.name,
        aliases: Array.from(v.aliases.entries()).map(([alias, fork]) => ({ alias, fork }))
      }))
      .sort((a, b) => {
        if (a.provider !== b.provider) return a.provider.localeCompare(b.provider);
        return a.name.localeCompare(b.name);
      });

    // 4. Extra aliases (no mapping yet)
    extraAliases.forEach((alias) => aliasSet.add(alias));

    // 5. Alias nodes: distinct by id = alias; sources = SourceNodes that have this alias in their aliases
    const aliasNodesList: AliasNode[] = Array.from(aliasSet)
      .map((alias) => ({
        id: alias,
        alias,
        sources: sources.filter((s) => s.aliases.some((entry) => entry.alias === alias))
      }))
      .sort((a, b) => {
        if (b.sources.length !== a.sources.length) return b.sources.length - a.sources.length;
        return a.alias.localeCompare(b.alias);
      });

    // 6. Group sources by provider
    const providerMap = new Map<string, SourceNode[]>();
    sources.forEach((s) => {
      if (!providerMap.has(s.provider)) providerMap.set(s.provider, []);
      providerMap.get(s.provider)!.push(s);
    });
    const providerNodesList = Array.from(providerMap.entries())
      .map(([provider, providerSources]) => ({ provider, sources: providerSources }))
      .sort((a, b) => a.provider.localeCompare(b.provider));

    return { aliasNodes: aliasNodesList, providerNodes: providerNodesList };
  }, [modelAlias, allProviderModels, extraAliases]);

  // Track element positions
  const providerRefs = useRef<Map<string, HTMLDivElement>>(new Map());
  const sourceRefs = useRef<Map<string, HTMLDivElement>>(new Map());
  const aliasRefs = useRef<Map<string, HTMLDivElement>>(new Map());

  const toggleProviderCollapse = (provider: string) => {
    setCollapsedProviders((prev) => {
      const next = new Set(prev);
      if (next.has(provider)) next.delete(provider);
      else next.add(provider);
      return next;
    });
  };

  // Calculate lines: provider→source, source→alias (when expanded); midpoint + linkData for source→alias
  const updateLines = useCallback(() => {
    if (!containerRef.current) return;
    const containerRect = containerRef.current.getBoundingClientRect();
    const newLines: { path: string; color: string; id: string }[] = [];
    const nextProviderGroupHeights: Record<string, number> = {};

    const bezier = (
      x1: number, y1: number,
      x2: number, y2: number
    ) => {
      const cpx1 = x1 + (x2 - x1) * 0.5;
      const cpx2 = x2 - (x2 - x1) * 0.5;
      return `M ${x1} ${y1} C ${cpx1} ${y1}, ${cpx2} ${y2}, ${x2} ${y2}`;
    };

    providerNodes.forEach(({ provider, sources }) => {
      const collapsed = collapsedProviders.has(provider);
      if (collapsed) return;

      if (sources.length > 0) {
        const firstEl = sourceRefs.current.get(sources[0].id);
        const lastEl = sourceRefs.current.get(sources[sources.length - 1].id);
        if (firstEl && lastEl) {
          const height = Math.max(0, Math.round(lastEl.getBoundingClientRect().bottom - firstEl.getBoundingClientRect().top));
          if (height > 0) nextProviderGroupHeights[provider] = height;
        }
      }

      const providerEl = providerRefs.current.get(provider);
      if (!providerEl) return;
      const providerRect = providerEl.getBoundingClientRect();
      const px = providerRect.right - containerRect.left;
      const py = providerRect.top + providerRect.height / 2 - containerRect.top;
      const color = getProviderColor(provider);

      // Provider → Source (branch link, no dot)
      sources.forEach((source) => {
        const sourceEl = sourceRefs.current.get(source.id);
        if (!sourceEl) return;
        const sourceRect = sourceEl.getBoundingClientRect();
        const sx = sourceRect.left - containerRect.left;
        const sy = sourceRect.top + sourceRect.height / 2 - containerRect.top;
        newLines.push({
          id: `provider-${provider}-source-${source.id}`,
          path: bezier(px, py, sx, sy),
          color
        });
      });
      // Source → Alias: one line per alias
      sources.forEach((source) => {
        if (!source.aliases || source.aliases.length === 0) return;
        
        source.aliases.forEach((aliasEntry) => {
          const sourceEl = sourceRefs.current.get(source.id);
          const aliasEl = aliasRefs.current.get(aliasEntry.alias);
          if (!sourceEl || !aliasEl) return;
          
          const sourceRect = sourceEl.getBoundingClientRect();
          const aliasRect = aliasEl.getBoundingClientRect();
          
          // Calculate coordinates relative to the container
          const x1 = sourceRect.right - containerRect.left;
          const y1 = sourceRect.top + sourceRect.height / 2 - containerRect.top;
          const x2 = aliasRect.left - containerRect.left;
          const y2 = aliasRect.top + aliasRect.height / 2 - containerRect.top;
          
          newLines.push({
            id: `${source.id}-${aliasEntry.alias}`,
            path: bezier(x1, y1, x2, y2),
            color
          });
        });
      });
    });

    setLines(newLines);
    setProviderGroupHeights((prev) => {
      const prevKeys = Object.keys(prev);
      const nextKeys = Object.keys(nextProviderGroupHeights);
      if (prevKeys.length !== nextKeys.length) return nextProviderGroupHeights;
      for (const key of nextKeys) {
        if (!(key in prev) || prev[key] !== nextProviderGroupHeights[key]) {
          return nextProviderGroupHeights;
        }
      }
      return prev;
    });
  }, [providerNodes, collapsedProviders]);

  useImperativeHandle(
    ref,
    () => ({
      collapseAll: () => setCollapsedProviders(new Set(providerNodes.map((p) => p.provider))),
      refreshLayout: () => updateLines()
    }),
    [providerNodes, updateLines]
  );

  useLayoutEffect(() => {
    // updateLines is called after layout is calculated, ensuring elements are in place.
    const raf = requestAnimationFrame(updateLines);
    window.addEventListener('resize', updateLines);
    return () => {
      cancelAnimationFrame(raf);
      window.removeEventListener('resize', updateLines);
    };
  }, [updateLines, aliasNodes]);

  useLayoutEffect(() => {
    const raf = requestAnimationFrame(updateLines);
    return () => cancelAnimationFrame(raf);
  }, [providerGroupHeights, updateLines]);

  useEffect(() => {
    if (!containerRef.current || typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(() => updateLines());
    observer.observe(containerRef.current);
    return () => observer.disconnect();
  }, [updateLines]);

  // Drag and Drop handlers
  // 1. Source -> Alias
  const handleDragStart = (e: DragEvent, source: SourceNode) => {
    setTapSourceId(null);
    setTapAlias(null);
    setDraggedSource(source);
    e.dataTransfer.setData('text/plain', source.id);
    e.dataTransfer.effectAllowed = 'link';
  };

  const handleDragOver = (e: DragEvent, alias: string) => {
    if (!draggedSource || draggedSource.aliases.some((entry) => entry.alias === alias)) return;
    e.preventDefault(); // Allow drop
    e.dataTransfer.dropEffect = 'link';
    setDropTargetAlias(alias);
  };

  const handleDragLeave = () => {
    setDropTargetAlias(null);
  };

  const handleDrop = (e: DragEvent, alias: string) => {
    e.preventDefault();
    if (draggedSource && !draggedSource.aliases.some((entry) => entry.alias === alias) && onUpdate) {
      onUpdate(draggedSource.provider, draggedSource.name, alias);
    }
    setDraggedSource(null);
    setDropTargetAlias(null);
  };

  // 2. Alias -> Source
  const handleDragStartAlias = (e: DragEvent, alias: string) => {
    setTapSourceId(null);
    setTapAlias(null);
    setDraggedAlias(alias);
    e.dataTransfer.setData('text/plain', alias);
    e.dataTransfer.effectAllowed = 'link';
  };

  const handleDragOverSource = (e: DragEvent, source: SourceNode) => {
    if (!draggedAlias || source.aliases.some((entry) => entry.alias === draggedAlias)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'link';
    setDropTargetSource(source.id);
  };

  const handleDragLeaveSource = () => {
    setDropTargetSource(null);
  };

  const handleDropOnSource = (e: DragEvent, source: SourceNode) => {
    e.preventDefault();
    if (draggedAlias && !source.aliases.some((entry) => entry.alias === draggedAlias) && onUpdate) {
      onUpdate(source.provider, source.name, draggedAlias);
    }
    setDraggedAlias(null);
    setDropTargetSource(null);
  };

  const handleContextMenu = (
    e: ReactMouseEvent,
    type: 'alias' | 'background' | 'provider' | 'source',
    data?: string
  ) => {
    e.preventDefault();
    e.stopPropagation();
    setContextMenu({
      x: e.clientX,
      y: e.clientY,
      type,
      data
    });
  };

  const closeContextMenu = () => setContextMenu(null);

  const resolveSourceById = useCallback(
    (id: string | null) => {
      if (!id) return null;
      for (const { sources } of providerNodes) {
        const found = sources.find((source) => source.id === id);
        if (found) return found;
      }
      return null;
    },
    [providerNodes]
  );

  const handleTapSelectSource = (source: SourceNode) => {
    if (!onUpdate) return;
    if (tapSourceId === source.id) {
      setTapSourceId(null);
      return;
    }

    if (tapAlias) {
      onUpdate(source.provider, source.name, tapAlias);
      setTapSourceId(null);
      setTapAlias(null);
      return;
    }

    setTapSourceId(source.id);
    setTapAlias(null);
  };

  const handleTapSelectAlias = (alias: string) => {
    if (!onUpdate) return;
    if (tapAlias === alias) {
      setTapAlias(null);
      return;
    }

    if (tapSourceId) {
      const source = resolveSourceById(tapSourceId);
      if (source) {
        onUpdate(source.provider, source.name, alias);
      }
      setTapSourceId(null);
      setTapAlias(null);
      return;
    }

    setTapAlias(alias);
    setTapSourceId(null);
  };

  const handleUnlinkSource = (provider: string, sourceModel: string, alias: string) => {
    if (onDeleteLink) onDeleteLink(provider, sourceModel, alias);
  };

  const handleToggleFork = (
    provider: string,
    sourceModel: string,
    alias: string,
    value: boolean
  ) => {
    if (onToggleFork) onToggleFork(provider, sourceModel, alias, value);
  };

  const handleAddAlias = () => {
    closeContextMenu();
    setAddAliasOpen(true);
    setAddAliasValue('');
    setAddAliasError('');
  };

  const handleAddAliasSubmit = () => {
    const trimmed = addAliasValue.trim();
    if (!trimmed) {
      setAddAliasError(t('oauth_model_alias.diagram_please_enter_alias'));
      return;
    }
    if (aliasNodes.some(a => a.alias === trimmed)) {
      setAddAliasError(t('oauth_model_alias.diagram_alias_exists'));
      return;
    }
    setExtraAliases(prev => [...prev, trimmed]);
    setAddAliasOpen(false);
  };

  const handleRenameClick = (oldAlias: string) => {
    closeContextMenu();
    setRenameState({ oldAlias });
    setRenameValue(oldAlias);
    setRenameError('');
  };

  const handleRenameSubmit = () => {
    const trimmed = renameValue.trim();
    if (!trimmed) {
      setRenameError(t('oauth_model_alias.diagram_please_enter_alias'));
      return;
    }
    if (trimmed === renameState?.oldAlias) {
      setRenameState(null);
      return;
    }
    if (aliasNodes.some(a => a.alias === trimmed)) {
      setRenameError(t('oauth_model_alias.diagram_alias_exists'));
      return;
    }
    if (onRenameAlias && renameState) onRenameAlias(renameState.oldAlias, trimmed);
    if (extraAliases.includes(renameState?.oldAlias ?? '')) {
      setExtraAliases(prev => prev.map(a => a === renameState?.oldAlias ? trimmed : a));
    }
    setRenameState(null);
  };

  const handleDeleteClick = (alias: string) => {
    closeContextMenu();
    const node = aliasNodes.find(n => n.alias === alias);
    if (!node) return;

    if (node.sources.length === 0) {
      setExtraAliases(prev => prev.filter(a => a !== alias));
    } else {
      if (onDeleteAlias) onDeleteAlias(alias);
    }
  };


  return (
    <div className={[styles.scrollContainer, className].filter(Boolean).join(' ')}>
      {enableTapLinking && onUpdate && (
        <div className={styles.tapHint}>{t('oauth_model_alias.diagram_tap_hint')}</div>
      )}
      <div
        className={styles.container}
        ref={containerRef}
        onContextMenu={(e) => {
          e.preventDefault();
          e.stopPropagation();
          handleContextMenu(e, 'background');
        }}
      >
        <svg className={styles.connections}>
          {lines.map((line) => (
            <path
              key={line.id}
              d={line.path}
              stroke={line.color}
              strokeOpacity={isDark ? 0.4 : 0.3}
            />
          ))}
        </svg>

        <ProviderColumn
          providerNodes={providerNodes}
          collapsedProviders={collapsedProviders}
          getProviderColor={getProviderColor}
          providerGroupHeights={providerGroupHeights}
          providerRefs={providerRefs}
          onToggleCollapse={toggleProviderCollapse}
          onContextMenu={(e, type, data) => handleContextMenu(e, type, data)}
          label={t('oauth_model_alias.diagram_providers')}
          expandLabel={t('oauth_model_alias.diagram_expand')}
          collapseLabel={t('oauth_model_alias.diagram_collapse')}
        />
        <SourceColumn
          providerNodes={providerNodes}
          collapsedProviders={collapsedProviders}
          sourceRefs={sourceRefs}
          getProviderColor={getProviderColor}
          selectedSourceId={enableTapLinking ? tapSourceId : null}
          onSelectSource={enableTapLinking ? handleTapSelectSource : undefined}
          draggedSource={draggedSource}
          dropTargetSource={dropTargetSource}
          draggable={!!onUpdate}
          onDragStart={handleDragStart}
          onDragEnd={() => {
            setDraggedSource(null);
            setDropTargetAlias(null);
          }}
          onDragOver={handleDragOverSource}
          onDragLeave={handleDragLeaveSource}
          onDrop={handleDropOnSource}
          onContextMenu={(e, type, data) => handleContextMenu(e, type, data)}
          label={t('oauth_model_alias.diagram_source_models')}
        />
        <AliasColumn
          aliasNodes={aliasNodes}
          aliasRefs={aliasRefs}
          dropTargetAlias={dropTargetAlias}
          draggedAlias={draggedAlias}
          selectedAlias={enableTapLinking ? tapAlias : null}
          onSelectAlias={enableTapLinking ? handleTapSelectAlias : undefined}
          draggable={!!onUpdate}
          onDragStart={handleDragStartAlias}
          onDragEnd={() => {
            setDraggedAlias(null);
            setDropTargetSource(null);
          }}
          onDragOver={handleDragOver}
          onDragLeave={handleDragLeave}
          onDrop={handleDrop}
          onContextMenu={(e, type, data) => handleContextMenu(e, type, data)}
          label={t('oauth_model_alias.diagram_aliases')}
        />
      </div>

      <DiagramContextMenu
        contextMenu={contextMenu}
        t={t}
        onRequestClose={() => setContextMenu(null)}
        onAddAlias={handleAddAlias}
        onRenameAlias={handleRenameClick}
        onOpenAliasSettings={(alias) => {
          setContextMenu(null);
          setSettingsAlias(alias);
        }}
        onDeleteAlias={handleDeleteClick}
        onEditProvider={(provider) => {
          setContextMenu(null);
          onEditProvider?.(provider);
        }}
        onDeleteProvider={(provider) => {
          setContextMenu(null);
          onDeleteProvider?.(provider);
        }}
        onOpenSourceSettings={(sourceId) => {
          setContextMenu(null);
          setSettingsSourceId(sourceId);
        }}
      />

      <RenameAliasModal
        open={!!renameState}
        t={t}
        value={renameValue}
        error={renameError}
        onChange={(value) => {
          setRenameValue(value);
          setRenameError('');
        }}
        onClose={() => setRenameState(null)}
        onSubmit={handleRenameSubmit}
      />
      <AddAliasModal
        open={addAliasOpen}
        t={t}
        value={addAliasValue}
        error={addAliasError}
        onChange={(value) => {
          setAddAliasValue(value);
          setAddAliasError('');
        }}
        onClose={() => setAddAliasOpen(false)}
        onSubmit={handleAddAliasSubmit}
      />
      <SettingsAliasModal
        open={Boolean(settingsAlias)}
        t={t}
        alias={settingsAlias}
        aliasNodes={aliasNodes}
        onClose={() => setSettingsAlias(null)}
        onToggleFork={handleToggleFork}
        onUnlink={handleUnlinkSource}
      />
      <SettingsSourceModal
        open={Boolean(settingsSourceId)}
        t={t}
        source={resolveSourceById(settingsSourceId)}
        onClose={() => setSettingsSourceId(null)}
        onToggleFork={handleToggleFork}
        onUnlink={handleUnlinkSource}
      />
    </div>
  );
});
