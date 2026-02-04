import { forwardRef, type ReactNode } from 'react';
import { Button } from '@/components/ui/Button';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { IconChevronLeft } from '@/components/ui/icons';
import styles from './SecondaryScreenShell.module.scss';

export type SecondaryScreenShellProps = {
  title: ReactNode;
  onBack?: () => void;
  backLabel?: string;
  backAriaLabel?: string;
  rightAction?: ReactNode;
  isLoading?: boolean;
  loadingLabel?: ReactNode;
  className?: string;
  contentClassName?: string;
  children?: ReactNode;
};

export const SecondaryScreenShell = forwardRef<HTMLDivElement, SecondaryScreenShellProps>(
  function SecondaryScreenShell(
    {
      title,
      onBack,
      backLabel = 'Back',
      backAriaLabel,
      rightAction,
      isLoading = false,
      loadingLabel = 'Loading...',
      className = '',
      contentClassName = '',
      children,
    },
    ref
  ) {
    const containerClassName = [styles.container, className].filter(Boolean).join(' ');
    const contentClasses = [styles.content, contentClassName].filter(Boolean).join(' ');
    const titleTooltip = typeof title === 'string' ? title : undefined;
    const resolvedBackAriaLabel = backAriaLabel ?? backLabel;

    return (
      <div className={containerClassName} ref={ref}>
        <div className={styles.topBar}>
          {onBack ? (
            <Button
              variant="ghost"
              size="sm"
              onClick={onBack}
              className={styles.backButton}
              aria-label={resolvedBackAriaLabel}
            >
              <span className={styles.backIcon}>
                <IconChevronLeft size={18} />
              </span>
              <span className={styles.backText}>{backLabel}</span>
            </Button>
          ) : (
            <div />
          )}
          <div className={styles.topBarTitle} title={titleTooltip}>
            {title}
          </div>
          <div className={styles.rightSlot}>{rightAction}</div>
        </div>

        {isLoading ? (
          <div className={styles.loadingState}>
            <LoadingSpinner size={16} />
            <span>{loadingLabel}</span>
          </div>
        ) : (
          <div className={contentClasses}>{children}</div>
        )}
      </div>
    );
  }
);

