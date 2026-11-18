import { useEffect, useId, useRef, useState, type DragEvent, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { IconEllipsis, IconGripVertical, IconLock } from '@/components/ui/icons';
import { PROVIDER_LOGOS } from '@/features/providers/brandLogos';
import type { ProviderBrand } from '@/features/providers/types';
import { getAuthFileIcon } from '@/features/authFiles/constants';
import type { ResolvedTheme } from '@/types';
import type { RouteMember, RoutingMember, RoutingStrategy } from '@/types/routing';
import { formatRoutingDuration } from '../format';
import styles from './MemberCard.module.scss';

const FAMILY_BRANDS: Record<string, ProviderBrand> = {
  gemini: 'gemini',
  interactions: 'interactions',
  codex: 'codex',
  meta: 'meta',
  xai: 'xai',
  claude: 'claude',
  vertex: 'vertex',
  'openai-compatibility': 'openaiCompatibility',
};

function memberIcon(member: RoutingMember, theme: ResolvedTheme): string | null {
  if (member.kind === 'credential') return getAuthFileIcon(member.family, theme);
  const logo = PROVIDER_LOGOS[FAMILY_BRANDS[member.family]];
  if (!logo) return null;
  return theme === 'dark' && logo.darkSrc ? logo.darkSrc : logo.src;
}

export interface MemberCardProps {
  member: RoutingMember;
  route: RouteMember | undefined;
  strategy: RoutingStrategy;
  weights: readonly (number | null)[];
  cooldownSeconds: number;
  theme: ResolvedTheme;
  canMoveUp: boolean;
  canMoveDown: boolean;
  dragging: boolean;
  onDragStart: (key: string) => void;
  onDragEnd: () => void;
  onStep: (key: string, direction: 'up' | 'down') => void;
  onEditWeights: (key: string) => void;
}

export function MemberCard({
  member,
  route,
  strategy,
  weights,
  cooldownSeconds,
  theme,
  canMoveUp,
  canMoveDown,
  dragging,
  onDragStart,
  onDragEnd,
  onStep,
  onEditWeights,
}: MemberCardProps) {
  const { t } = useTranslation();
  const [menuOpen, setMenuOpen] = useState(false);
  const menuId = useId();
  const rootRef = useRef<HTMLDivElement>(null);
  const menuButtonRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const icon = memberIcon(member, theme);
  const state = route?.state;

  useEffect(() => {
    if (!menuOpen) return;
    menuRef.current?.querySelector<HTMLButtonElement>('button:not(:disabled)')?.focus();
    const close = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setMenuOpen(false);
    };
    document.addEventListener('pointerdown', close);
    return () => document.removeEventListener('pointerdown', close);
  }, [menuOpen]);

  const closeMenu = (restoreFocus: boolean) => {
    setMenuOpen(false);
    if (restoreFocus) menuButtonRef.current?.focus();
  };

  const runMenuAction = (action: () => void) => {
    closeMenu(true);
    action();
  };

  const handleMenuKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Escape') {
      event.stopPropagation();
      closeMenu(true);
      return;
    }
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
    event.preventDefault();
    const items = Array.from(
      menuRef.current?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)') ?? []
    );
    const index = items.indexOf(document.activeElement as HTMLButtonElement);
    const next = event.key === 'ArrowDown' ? index + 1 : index - 1;
    items[(next + items.length) % items.length]?.focus();
  };

  const handleHandleKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (event.key === 'ArrowUp' && canMoveUp) {
      event.preventDefault();
      onStep(member.key, 'up');
    } else if (event.key === 'ArrowDown' && canMoveDown) {
      event.preventDefault();
      onStep(member.key, 'down');
    }
  };

  const handleDragStart = (event: DragEvent<HTMLDivElement>) => {
    if (member.locked) {
      event.preventDefault();
      return;
    }
    event.dataTransfer.effectAllowed = 'move';
    event.dataTransfer.setData('text/plain', member.key);
    onDragStart(member.key);
  };

  const kindLabel =
    member.kind === 'credential'
      ? t('routing.kind_oauth')
      : member.kind === 'compat'
        ? t('routing.kind_compat_keys', { count: member.keyCount })
        : t('routing.kind_api_key');

  // Disabled is configuration, reliable for every member. Cooldown and availability are runtime
  // state CPA exposes only for credentials, so an enabled config API key says it has none. That
  // note lives in the card's title (and the entry InfoTip): repeating it on every card is only noise.
  const showNoRuntimeState = member.kind !== 'credential' && !member.disabled;

  let chip: string | null = null;
  if (state === 'disabled') chip = t('routing.state_disabled');
  else if (state === 'cooling') {
    chip = t('routing.state_cooling', { time: formatRoutingDuration(t, cooldownSeconds) });
  } else if (state === 'blocked') chip = t('routing.state_blocked');
  else if (state === 'unknown') chip = t('routing.state_unknown');
  else if (state === 'excluded') chip = t('routing.state_excluded');

  const singleWeight = member.weights.length === 1 ? weights[0] : null;
  const showWeight =
    strategy === 'weighted-round-robin' &&
    singleWeight !== null &&
    singleWeight !== undefined &&
    state !== 'excluded';
  const muted = state === 'disabled' || state === 'ineligible';

  const className = [
    styles.card,
    route?.share !== null && route?.share !== undefined ? styles.serving : '',
    muted ? styles.muted : '',
    dragging ? styles.dragging : '',
    member.locked ? styles.locked : '',
  ]
    .filter(Boolean)
    .join(' ');

  const tooltip = [
    member.name,
    member.detail,
    state === 'ineligible' ? t('routing.state_ineligible') : '',
    showNoRuntimeState ? t('routing.no_runtime_state') : '',
  ]
    .filter(Boolean)
    .join('\n');

  return (
    <div
      ref={rootRef}
      className={className}
      draggable={!member.locked}
      onDragStart={handleDragStart}
      onDragEnd={onDragEnd}
      title={tooltip}
      data-member-key={member.key}
      data-state={state}
    >
      {member.locked ? (
        <span
          className={styles.handle}
          role="img"
          aria-label={t('routing.locked')}
          title={t('routing.locked')}
        >
          <IconLock size={13} />
        </span>
      ) : (
        <button
          type="button"
          className={styles.handle}
          aria-label={t('routing.drag_handle', { name: member.name })}
          onKeyDown={handleHandleKeyDown}
        >
          <IconGripVertical size={14} />
        </button>
      )}

      <span className={styles.icon} aria-hidden="true">
        {icon ? <img src={icon} alt="" /> : <span>{member.name.charAt(0).toUpperCase()}</span>}
      </span>

      <span className={styles.body}>
        <span className={styles.name}>{member.name}</span>
        <span className={styles.kind}>
          {kindLabel}
          {showWeight ? (
            <span className={styles.weight}>
              {' · '}
              {t('routing.weight_badge', { weight: singleWeight })}
            </span>
          ) : null}
        </span>
      </span>

      {route?.share !== null && route?.share !== undefined ? (
        <span className={styles.share}>{t('routing.share', { share: route.share })}</span>
      ) : null}
      {chip ? <span className={styles.chip}>{chip}</span> : null}

      <span className={styles.menuWrap}>
        <button
          ref={menuButtonRef}
          type="button"
          className={styles.menuButton}
          aria-label={t('routing.member_actions', { name: member.name })}
          aria-haspopup="menu"
          aria-expanded={menuOpen}
          aria-controls={menuOpen ? menuId : undefined}
          onClick={() => setMenuOpen((open) => !open)}
        >
          <IconEllipsis size={16} />
        </button>
        {menuOpen ? (
          <div
            ref={menuRef}
            id={menuId}
            role="menu"
            className={styles.menu}
            onKeyDown={handleMenuKeyDown}
          >
            {member.locked ? <p className={styles.menuNote}>{t('routing.locked')}</p> : null}
            <button
              type="button"
              role="menuitem"
              disabled={member.locked || !canMoveUp}
              onClick={() => runMenuAction(() => onStep(member.key, 'up'))}
            >
              {t('routing.move_up')}
            </button>
            <button
              type="button"
              role="menuitem"
              disabled={member.locked || !canMoveDown}
              onClick={() => runMenuAction(() => onStep(member.key, 'down'))}
            >
              {t('routing.move_down')}
            </button>
            {member.weights.length > 0 ? (
              <button
                type="button"
                role="menuitem"
                disabled={member.locked}
                onClick={() => runMenuAction(() => onEditWeights(member.key))}
              >
                {t('routing.edit_weight')}
              </button>
            ) : null}
          </div>
        ) : null}
      </span>
    </div>
  );
}
