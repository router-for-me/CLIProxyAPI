import type { CSSProperties } from 'react';

const STATUS_COLORS: Record<string, { bg: string; text: string }> = {
  available: { bg: 'rgba(34, 197, 94, 0.15)', text: 'rgb(34, 197, 94)' },
  used: { bg: 'rgba(59, 130, 246, 0.15)', text: 'rgb(59, 130, 246)' },
  banned: { bg: 'rgba(239, 68, 68, 0.15)', text: 'rgb(239, 68, 68)' },
  'region-unmatch': { bg: 'rgba(245, 158, 11, 0.15)', text: 'rgb(245, 158, 11)' },
  'auth-error': { bg: 'rgba(239, 68, 68, 0.15)', text: 'rgb(239, 68, 68)' },
  'oauth-success': { bg: 'rgba(34, 197, 94, 0.15)', text: 'rgb(34, 197, 94)' },
  credential: { bg: 'rgba(168, 85, 247, 0.15)', text: 'rgb(168, 85, 247)' },
};

const defaultColor = { bg: 'rgba(156, 163, 175, 0.15)', text: 'rgb(156, 163, 175)' };

export function StatusBadge({ status }: { status: string }) {
  const color = STATUS_COLORS[status] || defaultColor;
  const style: CSSProperties = {
    display: 'inline-block',
    padding: '2px 8px',
    borderRadius: '4px',
    fontSize: '12px',
    fontWeight: 500,
    backgroundColor: color.bg,
    color: color.text,
    whiteSpace: 'nowrap',
  };
  return <span style={style}>{status}</span>;
}
