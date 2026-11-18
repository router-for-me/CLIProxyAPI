import type { ReactNode } from 'react';
import { useRevealGroup } from '@/hooks/motion';
import styles from './PluginList.module.scss';

/**
 * Mounted once a view's first list arrives. `useRevealGroup` only collects `[data-reveal]`
 * children on mount, so the stagger plays for that first load and never for refreshes, search
 * filtering or view switches, which re-render (or merely re-show) this same instance.
 */
export function PluginListReveal({ children }: { children: ReactNode }) {
  const revealRef = useRevealGroup<HTMLDivElement>();
  return (
    <div ref={revealRef} className={styles.listArea}>
      {children}
    </div>
  );
}
