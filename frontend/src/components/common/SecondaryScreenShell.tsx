import { forwardRef, useLayoutEffect, useRef, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { Button } from '@/components/ui/Button';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { IconChevronLeft } from '@/components/ui/icons';
import { usePageTransitionLayer } from './PageTransitionLayer';
import styles from './SecondaryScreenShell.module.scss';

export type SecondaryScreenShellProps = {
  title: ReactNode;
  onBack?: () => void;
  backLabel?: string;
  backAriaLabel?: string;
  rightAction?: ReactNode;
  hideTopBarBackButton?: boolean;
  hideTopBarRightAction?: boolean;
  floatingAction?: ReactNode;
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
      hideTopBarBackButton = false,
      hideTopBarRightAction = false,
      floatingAction,
      isLoading = false,
      loadingLabel = 'Loading...',
      className = '',
      contentClassName = '',
      children,
    },
    ref
  ) {
    const containerClassName = [styles.container, className].filter(Boolean).join(' ');
    const contentClasses = [
      styles.content,
      floatingAction ? styles.contentWithFloatingAction : '',
      contentClassName,
    ]
      .filter(Boolean)
      .join(' ');
    const titleTooltip = typeof title === 'string' ? title : undefined;
    const resolvedBackAriaLabel = backAriaLabel ?? backLabel;
    const pageTransitionLayer = usePageTransitionLayer();
    const isCurrentLayer = pageTransitionLayer ? pageTransitionLayer.status === 'current' : true;
    const shouldRenderFloatingAction = Boolean(floatingAction) && isCurrentLayer;
    const floatingActionRef = useRef<HTMLDivElement | null>(null);

    useLayoutEffect(() => {
      if (!shouldRenderFloatingAction) return;

      const element = floatingActionRef.current;
      if (!element) return;

      const updateHeight = () => {
        const height = element.getBoundingClientRect().height;
        document.documentElement.style.setProperty(
          '--secondary-shell-floating-action-height',
          `${height}px`
        );
      };

      updateHeight();
      window.addEventListener('resize', updateHeight);

      const resizeObserver =
        typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(updateHeight);
      resizeObserver?.observe(element);

      return () => {
        resizeObserver?.disconnect();
        window.removeEventListener('resize', updateHeight);
        document.documentElement.style.removeProperty('--secondary-shell-floating-action-height');
      };
    }, [shouldRenderFloatingAction]);

    return (
      <>
        <div className={containerClassName} ref={ref}>
          <div className={styles.topBar}>
            {onBack && !hideTopBarBackButton ? (
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
            <div className={styles.rightSlot}>{hideTopBarRightAction ? null : rightAction}</div>
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
        {shouldRenderFloatingAction && typeof document !== 'undefined'
          ? createPortal(
              <div className={styles.floatingActionContainer}>
                <div className={styles.floatingActionSurface} ref={floatingActionRef}>
                  {floatingAction}
                </div>
              </div>,
              document.body
            )
          : null}
      </>
    );
  }
);
