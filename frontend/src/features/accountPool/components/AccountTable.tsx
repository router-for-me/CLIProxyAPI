import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import { StatusBadge } from './StatusBadge';
import type { MemberAccount, LeaderAccount } from '@/services/api/accountPool';

const ACCOUNT_STATUSES = [
  'available', 'used', 'banned', 'region-unmatch', 'auth-error', 'oauth-success', 'credential',
];

type Account = MemberAccount | LeaderAccount;

interface Props {
  items: Account[];
  type: 'member' | 'leader';
  onUpdateStatus: (id: number, status: string) => Promise<void>;
  onDelete: (id: number) => Promise<void>;
  onUpdate: (id: number, data: Partial<Account>) => Promise<void>;
}

export function CopyButton({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);
  if (!value) return null;

  const handleCopy = async (e: React.MouseEvent) => {
    e.stopPropagation();
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch { /* ignore */ }
  };

  return (
    <button
      onClick={handleCopy}
      title="Copy to clipboard"
      style={{
        border: 'none',
        background: 'none',
        cursor: 'pointer',
        padding: '2px 4px',
        fontSize: '12px',
        color: copied ? 'var(--success-color, #22c55e)' : 'var(--text-secondary)',
        borderRadius: '3px',
        lineHeight: 1,
      }}
    >
      {copied ? '✓' : '⎘'}
    </button>
  );
}

function MaskedField({ value }: { value: string }) {
  if (!value) return <span style={{ color: 'var(--text-tertiary)' }}>-</span>;
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: '4px' }}>
      <span style={{ fontFamily: 'monospace', fontSize: '12px', color: 'var(--text-tertiary)' }}>
        {'••••••••'}
      </span>
      <CopyButton value={value} />
    </span>
  );
}

function CopyableText({ value, mono }: { value: string; mono?: boolean }) {
  if (!value) return <span style={{ color: 'var(--text-tertiary)' }}>-</span>;
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: '4px' }}>
      <span style={{ fontFamily: mono ? 'monospace' : 'inherit', fontSize: '12px' }}>{value}</span>
      <CopyButton value={value} />
    </span>
  );
}

export function AccountTable({ items, type, onUpdateStatus, onDelete, onUpdate }: Props) {
  const { t } = useTranslation();
  const [editingId, setEditingId] = useState<number | null>(null);
  const [editData, setEditData] = useState<Record<string, string>>({});
  const [saving, setSaving] = useState(false);

  const startEdit = (item: Account) => {
    setEditingId(item.id);
    setEditData({
      email: item.email,
      password: item.password,
      recovery_email: item.recovery_email,
      totp_secret: item.totp_secret,
      nstbrowser_profile_id: item.nstbrowser_profile_id,
      nstbrowser_profile_name: item.nstbrowser_profile_name,
      ...('ultra_subscription_expiry' in item ? { ultra_subscription_expiry: item.ultra_subscription_expiry || '' } : {}),
    });
  };

  const cancelEdit = () => {
    setEditingId(null);
    setEditData({});
  };

  const saveEdit = async (id: number) => {
    setSaving(true);
    try {
      await onUpdate(id, editData);
      setEditingId(null);
    } finally {
      setSaving(false);
    }
  };

  const cellStyle: React.CSSProperties = {
    padding: '8px 10px',
    borderBottom: '1px solid var(--border-color)',
    fontSize: '13px',
    whiteSpace: 'nowrap',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    maxWidth: '200px',
  };

  const inputStyle: React.CSSProperties = {
    width: '100%',
    padding: '4px 6px',
    border: '1px solid var(--border-color)',
    borderRadius: '4px',
    backgroundColor: 'var(--bg-secondary)',
    color: 'var(--text-primary)',
    fontSize: '12px',
  };

  return (
    <div style={{ overflowX: 'auto' }}>
      <table style={{ width: '100%', borderCollapse: 'collapse' }}>
        <thead>
          <tr style={{ backgroundColor: 'var(--bg-secondary)' }}>
            <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left', maxWidth: '60px' }}>ID</th>
            <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>Email</th>
            <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>Password</th>
            <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>Recovery</th>
            <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>TOTP</th>
            <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>Status</th>
            <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>Profile ID</th>
            <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>Profile Name</th>
            {type === 'leader' && (
              <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>Ultra Expiry</th>
            )}
            <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'right' }}>
              {t('common.actions', { defaultValue: 'Actions' })}
            </th>
          </tr>
        </thead>
        <tbody>
          {items.map((item) => {
            const isEditing = editingId === item.id;
            return (
              <tr key={item.id} style={{ backgroundColor: isEditing ? 'var(--bg-hover)' : undefined }}>
                <td style={{ ...cellStyle, maxWidth: '60px', color: 'var(--text-tertiary)', fontSize: '12px' }}>{item.id}</td>
                <td style={cellStyle}>
                  {isEditing ? (
                    <input style={inputStyle} value={editData.email || ''} onChange={(e) => setEditData({ ...editData, email: e.target.value })} />
                  ) : (
                    <CopyableText value={item.email} mono />
                  )}
                </td>
                <td style={cellStyle}>
                  {isEditing ? (
                    <input style={inputStyle} value={editData.password || ''} onChange={(e) => setEditData({ ...editData, password: e.target.value })} />
                  ) : (
                    <MaskedField value={item.password} />
                  )}
                </td>
                <td style={cellStyle}>
                  {isEditing ? (
                    <input style={inputStyle} value={editData.recovery_email || ''} onChange={(e) => setEditData({ ...editData, recovery_email: e.target.value })} />
                  ) : (
                    <span style={{ fontSize: '12px' }}>{item.recovery_email || '-'}</span>
                  )}
                </td>
                <td style={cellStyle}>
                  {isEditing ? (
                    <input style={inputStyle} value={editData.totp_secret || ''} onChange={(e) => setEditData({ ...editData, totp_secret: e.target.value })} />
                  ) : (
                    <MaskedField value={item.totp_secret} />
                  )}
                </td>
                <td style={cellStyle}>
                  {isEditing ? (
                    <Select
                      value={editData.status || item.status}
                      onChange={(v) => setEditData({ ...editData, status: v })}
                      options={ACCOUNT_STATUSES.map((s) => ({ value: s, label: s }))}
                    />
                  ) : (
                    <StatusBadge status={item.status} />
                  )}
                </td>
                <td style={cellStyle}>
                  {isEditing ? (
                    <input style={inputStyle} value={editData.nstbrowser_profile_id || ''} onChange={(e) => setEditData({ ...editData, nstbrowser_profile_id: e.target.value })} />
                  ) : (
                    <span style={{ fontSize: '12px' }}>{item.nstbrowser_profile_id || '-'}</span>
                  )}
                </td>
                <td style={cellStyle}>
                  {isEditing ? (
                    <input style={inputStyle} value={editData.nstbrowser_profile_name || ''} onChange={(e) => setEditData({ ...editData, nstbrowser_profile_name: e.target.value })} />
                  ) : (
                    <span style={{ fontSize: '12px' }}>{item.nstbrowser_profile_name || '-'}</span>
                  )}
                </td>
                {type === 'leader' && (
                  <td style={cellStyle}>
                    {isEditing ? (
                      <input
                        type="date"
                        style={inputStyle}
                        value={editData.ultra_subscription_expiry?.split('T')[0] || ''}
                        onChange={(e) => setEditData({ ...editData, ultra_subscription_expiry: e.target.value ? e.target.value + 'T00:00:00Z' : '' })}
                      />
                    ) : (
                      <span style={{ fontSize: '12px' }}>
                        {('ultra_subscription_expiry' in item && item.ultra_subscription_expiry)
                          ? new Date(item.ultra_subscription_expiry).toLocaleDateString()
                          : '-'}
                      </span>
                    )}
                  </td>
                )}
                <td style={{ ...cellStyle, textAlign: 'right', whiteSpace: 'nowrap' }}>
                  {isEditing ? (
                    <span style={{ display: 'inline-flex', gap: '4px' }}>
                      <Button size="sm" onClick={() => saveEdit(item.id)} disabled={saving}>
                        {t('common.save', { defaultValue: 'Save' })}
                      </Button>
                      <Button size="sm" variant="secondary" onClick={cancelEdit}>
                        {t('common.cancel', { defaultValue: 'Cancel' })}
                      </Button>
                    </span>
                  ) : (
                    <span style={{ display: 'inline-flex', gap: '4px' }}>
                      <Button size="sm" variant="secondary" onClick={() => startEdit(item)}>
                        {t('common.edit', { defaultValue: 'Edit' })}
                      </Button>
                      <Select
                        value={item.status}
                        onChange={(v) => onUpdateStatus(item.id, v)}
                        options={ACCOUNT_STATUSES.map((s) => ({ value: s, label: s }))}
                      />
                      <Button size="sm" variant="danger" onClick={() => onDelete(item.id)}>
                        {t('common.delete', { defaultValue: 'Delete' })}
                      </Button>
                    </span>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {items.length === 0 && (
        <div style={{ textAlign: 'center', padding: '40px 0', color: 'var(--text-tertiary)' }}>
          No accounts found
        </div>
      )}
    </div>
  );
}
