import { useEffect } from 'react';
import { prefersReducedMotion } from '@/hooks/motion';
import {
  getLoginTransitionTimings,
  useLoginTransitionStore,
} from '@/stores/useLoginTransitionStore';
import './LoginRevealVeil.scss';

export function LoginRevealVeil() {
  const phase = useLoginTransitionStore((state) => state.phase);
  const revealId = useLoginTransitionStore((state) => state.revealId);
  const finishReveal = useLoginTransitionStore((state) => state.finishReveal);

  useEffect(() => {
    if (phase !== 'revealing') return;
    // 兜底：animationend 在后台标签页或被禁用动画时可能不触发
    const { reveal } = getLoginTransitionTimings(prefersReducedMotion());
    const timer = window.setTimeout(finishReveal, reveal + 120);
    return () => window.clearTimeout(timer);
  }, [phase, revealId, finishReveal]);

  if (phase !== 'revealing') return null;

  return <LoginRevealVeilLayer key={revealId} onDone={finishReveal} />;
}

export function LoginRevealVeilLayer({ onDone }: { onDone: () => void }) {
  return (
    <div
      className="login-reveal-veil"
      aria-hidden="true"
      onAnimationEnd={(event) => {
        if (event.target === event.currentTarget) onDone();
      }}
    />
  );
}
