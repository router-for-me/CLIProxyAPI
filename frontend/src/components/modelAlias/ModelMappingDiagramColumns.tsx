import type { DragEvent, MouseEvent as ReactMouseEvent, RefObject } from 'react';
import type { AliasNode, ProviderNode, SourceNode } from './ModelMappingDiagramTypes';
import styles from './ModelMappingDiagram.module.scss';

interface ProviderColumnProps {
  providerNodes: ProviderNode[];
  collapsedProviders: Set<string>;
  getProviderColor: (provider: string) => string;
  providerGroupHeights?: Record<string, number>;
  providerRefs: RefObject<Map<string, HTMLDivElement>>;
  onToggleCollapse: (provider: string) => void;
  onContextMenu: (e: ReactMouseEvent, type: 'provider' | 'background', data?: string) => void;
  label: string;
  expandLabel: string;
  collapseLabel: string;
}

export function ProviderColumn({
  providerNodes,
  collapsedProviders,
  getProviderColor,
  providerGroupHeights = {},
  providerRefs,
  onToggleCollapse,
  onContextMenu,
  label,
  expandLabel,
  collapseLabel
}: ProviderColumnProps) {
  return (
    <div
      className={`${styles.column} ${styles.providers}`}
      onContextMenu={(e) => {
        e.preventDefault();
        e.stopPropagation();
        onContextMenu(e, 'background');
      }}
    >
      <div className={styles.columnHeader}>{label}</div>
      {providerNodes.map(({ provider, sources }) => {
        const collapsed = collapsedProviders.has(provider);
        const groupHeight = collapsed ? undefined : providerGroupHeights[provider];
        return (
          <div
            key={provider}
            className={styles.providerGroup}
            style={groupHeight ? { height: groupHeight } : undefined}
          >
            <div
              ref={(el) => {
                if (el) providerRefs.current?.set(provider, el);
                else providerRefs.current?.delete(provider);
              }}
              className={`${styles.item} ${styles.providerItem}`}
              style={{ borderLeftColor: getProviderColor(provider) }}
              onContextMenu={(e) => {
                e.preventDefault();
                e.stopPropagation();
                onContextMenu(e, 'provider', provider);
              }}
            >
              <button
                type="button"
                className={styles.collapseBtn}
                onClick={() => onToggleCollapse(provider)}
                aria-label={collapsed ? expandLabel : collapseLabel}
                title={collapsed ? expandLabel : collapseLabel}
              >
                <span className={collapsed ? styles.chevronRight : styles.chevronDown} />
              </button>
              <span className={styles.providerLabel} style={{ color: getProviderColor(provider) }}>
                {provider}
              </span>
              <span className={styles.itemCount}>{sources.length}</span>
            </div>
          </div>
        );
      })}
    </div>
  );
}

interface SourceColumnProps {
  providerNodes: ProviderNode[];
  collapsedProviders: Set<string>;
  sourceRefs: RefObject<Map<string, HTMLDivElement>>;
  getProviderColor: (provider: string) => string;
  selectedSourceId?: string | null;
  onSelectSource?: (source: SourceNode) => void;
  draggedSource: SourceNode | null;
  dropTargetSource: string | null;
  draggable: boolean;
  onDragStart: (e: DragEvent, source: SourceNode) => void;
  onDragEnd: () => void;
  onDragOver: (e: DragEvent, source: SourceNode) => void;
  onDragLeave: () => void;
  onDrop: (e: DragEvent, source: SourceNode) => void;
  onContextMenu: (e: ReactMouseEvent, type: 'source' | 'background', data?: string) => void;
  label: string;
}

export function SourceColumn({
  providerNodes,
  collapsedProviders,
  sourceRefs,
  getProviderColor,
  selectedSourceId,
  onSelectSource,
  draggedSource,
  dropTargetSource,
  draggable,
  onDragStart,
  onDragEnd,
  onDragOver,
  onDragLeave,
  onDrop,
  onContextMenu,
  label
}: SourceColumnProps) {
  return (
    <div
      className={`${styles.column} ${styles.sources}`}
      onContextMenu={(e) => {
        e.preventDefault();
        e.stopPropagation();
        onContextMenu(e, 'background');
      }}
    >
      <div className={styles.columnHeader}>{label}</div>
      {providerNodes.flatMap(({ provider, sources }) => {
        if (collapsedProviders.has(provider)) return [];
        return sources.map((source) => (
          <div
            key={source.id}
            ref={(el) => {
              if (el) sourceRefs.current?.set(source.id, el);
              else sourceRefs.current?.delete(source.id);
            }}
            className={`${styles.item} ${styles.sourceItem} ${
              draggedSource?.id === source.id ? styles.dragging : ''
            } ${dropTargetSource === source.id ? styles.dropTarget : ''} ${
              selectedSourceId === source.id ? styles.selected : ''
            }`}
            onClick={() => onSelectSource?.(source)}
            draggable={draggable}
            onDragStart={(e) => onDragStart(e, source)}
            onDragEnd={onDragEnd}
            onDragOver={(e) => onDragOver(e, source)}
            onDragLeave={onDragLeave}
            onDrop={(e) => onDrop(e, source)}
            onContextMenu={(e) => {
              e.preventDefault();
              e.stopPropagation();
              onContextMenu(e, 'source', source.id);
            }}
          >
            <span className={styles.itemName} title={source.name}>
              {source.name}
            </span>
            <div
              className={styles.dot}
              style={{
                background: getProviderColor(source.provider),
                opacity: source.aliases.length > 0 ? 1 : 0.3
              }}
            />
          </div>
        ));
      })}
    </div>
  );
}

interface AliasColumnProps {
  aliasNodes: AliasNode[];
  aliasRefs: RefObject<Map<string, HTMLDivElement>>;
  dropTargetAlias: string | null;
  draggedAlias: string | null;
  selectedAlias?: string | null;
  onSelectAlias?: (alias: string) => void;
  draggable: boolean;
  onDragStart: (e: DragEvent, alias: string) => void;
  onDragEnd: () => void;
  onDragOver: (e: DragEvent, alias: string) => void;
  onDragLeave: () => void;
  onDrop: (e: DragEvent, alias: string) => void;
  onContextMenu: (e: ReactMouseEvent, type: 'alias' | 'background', data?: string) => void;
  label: string;
}

export function AliasColumn({
  aliasNodes,
  aliasRefs,
  dropTargetAlias,
  draggedAlias,
  selectedAlias,
  onSelectAlias,
  draggable,
  onDragStart,
  onDragEnd,
  onDragOver,
  onDragLeave,
  onDrop,
  onContextMenu,
  label
}: AliasColumnProps) {
  return (
    <div
      className={`${styles.column} ${styles.aliases}`}
      onContextMenu={(e) => {
        e.preventDefault();
        e.stopPropagation();
        onContextMenu(e, 'background');
      }}
    >
      <div className={styles.columnHeader}>{label}</div>
      {aliasNodes.map((node) => (
        <div
          key={node.id}
          ref={(el) => {
            if (el) aliasRefs.current?.set(node.id, el);
            else aliasRefs.current?.delete(node.id);
          }}
          className={`${styles.item} ${styles.aliasItem} ${
            dropTargetAlias === node.alias ? styles.dropTarget : ''
          } ${draggedAlias === node.alias ? styles.dragging : ''} ${
            selectedAlias === node.alias ? styles.selected : ''
          }`}
          onClick={() => onSelectAlias?.(node.alias)}
          draggable={draggable}
          onDragStart={(e) => onDragStart(e, node.alias)}
          onDragEnd={onDragEnd}
          onDragOver={(e) => onDragOver(e, node.alias)}
          onDragLeave={onDragLeave}
          onDrop={(e) => onDrop(e, node.alias)}
          onContextMenu={(e) => {
            e.preventDefault();
            e.stopPropagation();
            onContextMenu(e, 'alias', node.alias);
          }}
        >
          <div className={`${styles.dot} ${styles.dotLeft}`} />
          <span className={styles.itemName} title={node.alias}>
            {node.alias}
          </span>
          <span className={styles.itemCount}>{node.sources.length}</span>
        </div>
      ))}
    </div>
  );
}
