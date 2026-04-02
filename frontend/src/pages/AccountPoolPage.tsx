import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import * as OTPAuth from 'otpauth';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { StatusBadge } from '@/features/accountPool/components/StatusBadge';
import { AccountTable, CopyButton } from '@/features/accountPool/components/AccountTable';
import { BatchImportModal } from '@/features/accountPool/components/BatchImportModal';
import { useMembersData, useLeadersData } from '@/features/accountPool/hooks/useAccountPoolData';
import { useProxiesData } from '@/features/accountPool/hooks/useProxiesData';
import { useGroupRunsData } from '@/features/accountPool/hooks/useGroupsData';
import { accountPoolApi } from '@/services/api/accountPool';
import { useNotificationStore } from '@/stores/useNotificationStore';

function CopyOTPButton({ secret }: { secret: string }) {
  const [copied, setCopied] = useState(false);
  const handleCopy = async (e: React.MouseEvent) => {
    e.stopPropagation();
    try {
      const totp = new OTPAuth.TOTP({
        secret: OTPAuth.Secret.fromBase32(secret.replace(/\s+/g, '').toUpperCase()),
        digits: 6, period: 30, algorithm: 'SHA1',
      });
      await navigator.clipboard.writeText(totp.generate());
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch { /* ignore */ }
  };
  return (
    <button onClick={handleCopy} title="Copy OTP code" style={{
      border: 'none', background: 'none', cursor: 'pointer', padding: '2px 4px',
      fontSize: '12px', color: copied ? 'var(--success-color, #22c55e)' : 'var(--text-secondary)',
      borderRadius: '3px', lineHeight: 1,
    }}>
      {copied ? '\u2713' : '\u2398'}
    </button>
  );
}

type TabKey = 'members' | 'leaders' | 'proxies' | 'group-runs';

const ACCOUNT_STATUSES = ['', 'available', 'used', 'banned', 'region-unmatch', 'auth-error', 'oauth-success', 'credential'];
const GROUP_RUN_STATUSES = ['', 'pending', 'running', 'completed', 'failed'];
const PROXY_TYPES = ['', 'leader', 'member'];

const tabStyle: React.CSSProperties = {
  padding: '8px 16px',
  border: 'none',
  borderBottom: '2px solid transparent',
  background: 'none',
  cursor: 'pointer',
  fontSize: '14px',
  fontWeight: 500,
  color: 'var(--text-secondary)',
  transition: 'color 0.15s, border-color 0.15s',
};

const activeTabStyle: React.CSSProperties = {
  ...tabStyle,
  color: 'var(--text-primary)',
  borderBottomColor: 'var(--accent-color, #3b82f6)',
};

const toolbarStyle: React.CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  gap: '8px',
  marginBottom: '12px',
  flexWrap: 'wrap',
};

const paginationStyle: React.CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  padding: '12px 0',
  fontSize: '13px',
  color: 'var(--text-secondary)',
};

export function AccountPoolPage() {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const [activeTab, setActiveTab] = useState<TabKey>('members');
  const [statusFilter, setStatusFilter] = useState('');
  const [typeFilter, setTypeFilter] = useState('');
  const [search, setSearch] = useState('');
  const [page, setPage] = useState(1);
  const [batchModalOpen, setBatchModalOpen] = useState(false);
  const [batchModalType, setBatchModalType] = useState<'member' | 'leader' | 'proxy'>('member');
  const pageSize = 50;

  const { members, total: membersTotal, loading: membersLoading, loadMembers } = useMembersData();
  const { leaders, total: leadersTotal, loading: leadersLoading, loadLeaders } = useLeadersData();
  const { proxies, total: proxiesTotal, loading: proxiesLoading, loadProxies } = useProxiesData();
  const { groupRuns, total: groupRunsTotal, loading: groupRunsLoading, loadGroupRuns, expandedRun, expandedRunId, detailLoading, toggleExpand } = useGroupRunsData();
  const [dateFilter, setDateFilter] = useState('');

  const reload = useCallback(() => {
    const offset = (page - 1) * pageSize;
    switch (activeTab) {
      case 'members':
        loadMembers({ status: statusFilter || undefined, search: search || undefined, limit: pageSize, offset });
        break;
      case 'leaders':
        loadLeaders({ status: statusFilter || undefined, search: search || undefined, limit: pageSize, offset });
        break;
      case 'proxies':
        loadProxies({ type: typeFilter || undefined, status: statusFilter || undefined, limit: pageSize, offset });
        break;
      case 'group-runs':
        loadGroupRuns({ date: dateFilter || undefined, status: statusFilter || undefined, limit: pageSize, offset });
        break;
    }
  }, [activeTab, statusFilter, typeFilter, search, dateFilter, page, pageSize, loadMembers, loadLeaders, loadProxies, loadGroupRuns]);

  useEffect(() => { reload(); }, [reload]);
  useHeaderRefresh(reload);

  useEffect(() => { setPage(1); setStatusFilter(''); setSearch(''); setTypeFilter(''); setDateFilter(''); }, [activeTab]);

  const currentTotal = activeTab === 'members' ? membersTotal : activeTab === 'leaders' ? leadersTotal : activeTab === 'proxies' ? proxiesTotal : groupRunsTotal;
  const totalPages = Math.max(1, Math.ceil(currentTotal / pageSize));
  const loading = activeTab === 'members' ? membersLoading : activeTab === 'leaders' ? leadersLoading : activeTab === 'proxies' ? proxiesLoading : groupRunsLoading;

  const handleUpdateMemberStatus = async (id: number, status: string) => {
    try {
      await accountPoolApi.updateMemberStatus(id, status);
      showNotification(t('account_pool.status_updated', { defaultValue: 'Status updated' }), 'success');
      reload();
    } catch (err) {
      showNotification((err as Error).message, 'error');
    }
  };

  const handleDeleteMember = async (id: number) => {
    try {
      await accountPoolApi.deleteMember(id);
      showNotification(t('account_pool.deleted', { defaultValue: 'Deleted' }), 'success');
      reload();
    } catch (err) {
      showNotification((err as Error).message, 'error');
    }
  };

  const handleUpdateMember = async (id: number, data: Record<string, unknown>) => {
    try {
      await accountPoolApi.updateMember(id, data);
      showNotification(t('account_pool.updated', { defaultValue: 'Updated' }), 'success');
      reload();
    } catch (err) {
      showNotification((err as Error).message, 'error');
    }
  };

  const handleUpdateLeaderStatus = async (id: number, status: string) => {
    try {
      await accountPoolApi.updateLeaderStatus(id, status);
      showNotification(t('account_pool.status_updated', { defaultValue: 'Status updated' }), 'success');
      reload();
    } catch (err) {
      showNotification((err as Error).message, 'error');
    }
  };

  const handleDeleteLeader = async (id: number) => {
    try {
      await accountPoolApi.deleteLeader(id);
      showNotification(t('account_pool.deleted', { defaultValue: 'Deleted' }), 'success');
      reload();
    } catch (err) {
      showNotification((err as Error).message, 'error');
    }
  };

  const handleUpdateLeader = async (id: number, data: Record<string, unknown>) => {
    try {
      await accountPoolApi.updateLeader(id, data);
      showNotification(t('account_pool.updated', { defaultValue: 'Updated' }), 'success');
      reload();
    } catch (err) {
      showNotification((err as Error).message, 'error');
    }
  };

  const handleDeleteProxy = async (id: number) => {
    try {
      await accountPoolApi.deleteProxy(id);
      showNotification(t('account_pool.deleted', { defaultValue: 'Deleted' }), 'success');
      reload();
    } catch (err) {
      showNotification((err as Error).message, 'error');
    }
  };

  const handleDeleteGroupRun = async (id: number) => {
    try {
      await accountPoolApi.deleteGroupRun(id);
      showNotification(t('account_pool.deleted', { defaultValue: 'Deleted' }), 'success');
      reload();
    } catch (err) {
      showNotification((err as Error).message, 'error');
    }
  };

  const handleBatchImport = async (text: string) => {
    try {
      let result;
      if (batchModalType === 'member') {
        result = await accountPoolApi.batchImportMembers(text);
      } else if (batchModalType === 'leader') {
        result = await accountPoolApi.batchImportLeaders(text);
      } else {
        result = await accountPoolApi.batchImportProxies(text, typeFilter || 'member');
      }
      const errors = result.errors?.length ? `\n${result.errors.join('\n')}` : '';
      showNotification(`Imported ${result.created} / ${result.total_lines}${errors}`, result.errors?.length ? 'warning' : 'success');
      reload();
    } catch (err) {
      showNotification((err as Error).message, 'error');
    }
  };

  const openBatchModal = (type: 'member' | 'leader' | 'proxy') => {
    setBatchModalType(type);
    setBatchModalOpen(true);
  };

  const batchPlaceholder = batchModalType === 'proxy'
    ? 'http://user:pass@host:port\nhttp://user:pass@host:port----leader'
    : 'email----password----recovery_email----totp_secret\nemail----password----totp_secret';

  const cellStyle: React.CSSProperties = {
    padding: '8px 10px',
    borderBottom: '1px solid var(--border-color)',
    fontSize: '13px',
  };

  return (
    <div style={{ padding: '0' }}>
      <div style={{ marginBottom: '16px' }}>
        <h1 style={{ fontSize: '20px', fontWeight: 600, margin: 0 }}>
          {t('account_pool.title', { defaultValue: 'Account Pool' })}
        </h1>
        <p style={{ fontSize: '13px', color: 'var(--text-secondary)', margin: '4px 0 0' }}>
          {t('account_pool.description', { defaultValue: 'Manage member accounts, leader accounts, proxies, and groups' })}
        </p>
      </div>

      {/* Tab bar */}
      <div style={{ display: 'flex', borderBottom: '1px solid var(--border-color)', marginBottom: '16px' }}>
        {(['members', 'leaders', 'proxies', 'group-runs'] as TabKey[]).map((tab) => {
          const label = tab === 'group-runs' ? 'Group Runs' : tab.charAt(0).toUpperCase() + tab.slice(1);
          const count = tab === 'members' ? membersTotal : tab === 'leaders' ? leadersTotal : tab === 'proxies' ? proxiesTotal : groupRunsTotal;
          return (
            <button key={tab} style={activeTab === tab ? activeTabStyle : tabStyle} onClick={() => setActiveTab(tab)}>
              {t(`account_pool.tab_${tab}`, { defaultValue: label })}
              <span style={{ marginLeft: '6px', fontSize: '11px', opacity: 0.6 }}>({count})</span>
            </button>
          );
        })}
      </div>

      <Card>
        {/* Toolbar */}
        <div style={toolbarStyle}>
          {(activeTab === 'members' || activeTab === 'leaders') && (
            <Input
              value={search}
              onChange={(e) => { setSearch(e.target.value); setPage(1); }}
              placeholder="Search email..."
              style={{ width: '200px' }}
            />
          )}
          {activeTab === 'group-runs' && (
            <Input
              type="date"
              value={dateFilter}
              onChange={(e) => { setDateFilter(e.target.value); setPage(1); }}
              style={{ width: '160px' }}
            />
          )}
          {(activeTab === 'members' || activeTab === 'leaders' || activeTab === 'proxies') && (
            <Select
              value={statusFilter}
              onChange={(v) => { setStatusFilter(v); setPage(1); }}
              options={ACCOUNT_STATUSES.map((s) => ({ value: s, label: s || 'All statuses' }))}
            />
          )}
          {activeTab === 'group-runs' && (
            <Select
              value={statusFilter}
              onChange={(v) => { setStatusFilter(v); setPage(1); }}
              options={GROUP_RUN_STATUSES.map((s) => ({ value: s, label: s || 'All statuses' }))}
            />
          )}
          {activeTab === 'proxies' && (
            <Select
              value={typeFilter}
              onChange={(v) => { setTypeFilter(v); setPage(1); }}
              options={PROXY_TYPES.map((s) => ({ value: s, label: s || 'All types' }))}
            />
          )}
          <div style={{ flex: 1 }} />
          {(activeTab === 'members' || activeTab === 'leaders' || activeTab === 'proxies') && (
            <Button onClick={() => openBatchModal(activeTab === 'proxies' ? 'proxy' : activeTab === 'leaders' ? 'leader' : 'member')}>
              {t('account_pool.batch_import', { defaultValue: 'Batch Import' })}
            </Button>
          )}
        </div>

        {/* Loading */}
        {loading && <div style={{ textAlign: 'center', padding: '20px', color: 'var(--text-secondary)' }}>Loading...</div>}

        {/* Members tab */}
        {activeTab === 'members' && !loading && (
          <AccountTable
            items={members}
            type="member"
            onUpdateStatus={handleUpdateMemberStatus}
            onDelete={handleDeleteMember}
            onUpdate={handleUpdateMember}
          />
        )}

        {/* Leaders tab */}
        {activeTab === 'leaders' && !loading && (
          <AccountTable
            items={leaders}
            type="leader"
            onUpdateStatus={handleUpdateLeaderStatus}
            onDelete={handleDeleteLeader}
            onUpdate={handleUpdateLeader}
          />
        )}

        {/* Proxies tab */}
        {activeTab === 'proxies' && !loading && (
          <div style={{ overflowX: 'auto' }}>
            <table style={{ width: '100%', borderCollapse: 'collapse' }}>
              <thead>
                <tr style={{ backgroundColor: 'var(--bg-secondary)' }}>
                  <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left', maxWidth: '60px' }}>ID</th>
                  <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>Proxy URL</th>
                  <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>Type</th>
                  <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>Status</th>
                  <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'right' }}>Actions</th>
                </tr>
              </thead>
              <tbody>
                {proxies.map((p) => (
                  <tr key={p.id}>
                    <td style={{ ...cellStyle, maxWidth: '60px', color: 'var(--text-tertiary)', fontSize: '12px' }}>{p.id}</td>
                    <td style={{ ...cellStyle, fontFamily: 'monospace', fontSize: '12px' }}>
                      <span style={{ display: 'inline-flex', alignItems: 'center', gap: '4px' }}>
                        {p.proxy_url}
                        <CopyButton value={p.proxy_url} />
                      </span>
                    </td>
                    <td style={cellStyle}><StatusBadge status={p.type} /></td>
                    <td style={cellStyle}><StatusBadge status={p.status} /></td>
                    <td style={{ ...cellStyle, textAlign: 'right' }}>
                      <Button size="sm" variant="danger" onClick={() => handleDeleteProxy(p.id)}>
                        Delete
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {proxies.length === 0 && (
              <div style={{ textAlign: 'center', padding: '40px 0', color: 'var(--text-tertiary)' }}>No proxies found</div>
            )}
          </div>
        )}

        {/* Group Runs tab */}
        {activeTab === 'group-runs' && !loading && (
          <div style={{ overflowX: 'auto' }}>
            <table style={{ width: '100%', borderCollapse: 'collapse' }}>
              <thead>
                <tr style={{ backgroundColor: 'var(--bg-secondary)' }}>
                  <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left', width: '30px' }}></th>
                  <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left', maxWidth: '60px' }}>ID</th>
                  <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>Group</th>
                  <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>Date</th>
                  <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>Leader ID</th>
                  <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>Status</th>
                  <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>To Remove</th>
                  <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'right' }}>Actions</th>
                </tr>
              </thead>
              <tbody>
                {groupRuns.map((r) => (
                  <>
                    <tr key={r.id} style={{ cursor: 'pointer' }} onClick={() => toggleExpand(r.id)}>
                      <td style={{ ...cellStyle, width: '30px', textAlign: 'center', fontSize: '12px' }}>
                        {expandedRunId === r.id ? '\u25BC' : '\u25B6'}
                      </td>
                      <td style={{ ...cellStyle, maxWidth: '60px', color: 'var(--text-tertiary)', fontSize: '12px' }}>{r.id}</td>
                      <td style={{ ...cellStyle, fontFamily: 'monospace', fontSize: '13px', fontWeight: 600 }}>{r.group_id}</td>
                      <td style={{ ...cellStyle, fontSize: '12px' }}>{r.run_date?.slice(0, 10)}</td>
                      <td style={{ ...cellStyle, fontSize: '12px' }}>{r.leader_id}</td>
                      <td style={cellStyle}><StatusBadge status={r.status} /></td>
                      <td style={{ ...cellStyle, fontSize: '12px' }}>
                        {r.to_remove && r.to_remove.length > 0
                          ? r.to_remove[0] === '*' ? 'All' : r.to_remove.length.toString()
                          : '-'}
                      </td>
                      <td style={{ ...cellStyle, textAlign: 'right' }} onClick={(e) => e.stopPropagation()}>
                        <Button size="sm" variant="danger" onClick={() => handleDeleteGroupRun(r.id)}>
                          Delete
                        </Button>
                      </td>
                    </tr>
                    {expandedRunId === r.id && (
                      <tr key={`${r.id}-detail`}>
                        <td colSpan={8} style={{ padding: 0, borderBottom: '1px solid var(--border-color)' }}>
                          {detailLoading ? (
                            <div style={{ padding: '12px 20px', color: 'var(--text-secondary)', fontSize: '13px' }}>Loading...</div>
                          ) : expandedRun ? (
                            <div style={{ backgroundColor: 'var(--bg-secondary)' }}>
                              {/* Leader info */}
                              <div style={{ padding: '10px 16px 6px 40px', fontSize: '12px', borderBottom: '1px solid var(--border-color)' }}>
                                <span style={{ fontWeight: 600, marginRight: '12px' }}>Leader:</span>
                                <span style={{ display: 'inline-flex', alignItems: 'center', gap: '4px', marginRight: '12px' }}>
                                  {expandedRun.leader.email}
                                  <CopyButton value={expandedRun.leader.email} />
                                </span>
                                <span style={{ color: 'var(--text-secondary)', marginRight: '4px' }}>pwd:</span>
                                <span style={{ display: 'inline-flex', alignItems: 'center', gap: '4px', fontFamily: 'monospace', marginRight: '12px' }}>
                                  {expandedRun.leader.password}
                                  <CopyButton value={expandedRun.leader.password} />
                                </span>
                                <span style={{ color: 'var(--text-secondary)', marginRight: '4px' }}>totp:</span>
                                <span style={{ display: 'inline-flex', alignItems: 'center', gap: '4px', fontFamily: 'monospace', marginRight: '12px' }}>
                                  {expandedRun.leader.totp_secret}
                                  <CopyButton value={expandedRun.leader.totp_secret} />
                                  <CopyOTPButton secret={expandedRun.leader.totp_secret} />
                                </span>
                                {expandedRun.leader.proxy && (
                                  <>
                                    <span style={{ color: 'var(--text-secondary)', marginRight: '4px' }}>proxy:</span>
                                    <span style={{ display: 'inline-flex', alignItems: 'center', gap: '4px', fontFamily: 'monospace', fontSize: '11px' }}>
                                      {expandedRun.leader.proxy}
                                      <CopyButton value={expandedRun.leader.proxy} />
                                    </span>
                                  </>
                                )}
                              </div>
                              {/* Members table */}
                              {expandedRun.members && expandedRun.members.length > 0 ? (
                                <table style={{ width: '100%', borderCollapse: 'collapse' }}>
                                  <thead>
                                    <tr>
                                      <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left', fontSize: '12px', paddingLeft: '40px' }}>Email</th>
                                      <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left', fontSize: '12px' }}>Password</th>
                                      <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left', fontSize: '12px' }}>TOTP Secret</th>
                                      <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left', fontSize: '12px' }}>Proxy</th>
                                      <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left', fontSize: '12px' }}>Port</th>
                                      <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left', fontSize: '12px' }}>Status</th>
                                      <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left', fontSize: '12px' }}>Message</th>
                                    </tr>
                                  </thead>
                                  <tbody>
                                    {expandedRun.members.map((m) => (
                                      <tr key={m.member_id}>
                                        <td style={{ ...cellStyle, fontSize: '12px', paddingLeft: '40px' }}>
                                          <span style={{ display: 'inline-flex', alignItems: 'center', gap: '4px' }}>
                                            {m.email}
                                            <CopyButton value={m.email} />
                                          </span>
                                        </td>
                                        <td style={{ ...cellStyle, fontFamily: 'monospace', fontSize: '12px' }}>
                                          <span style={{ display: 'inline-flex', alignItems: 'center', gap: '4px' }}>
                                            {m.password}
                                            <CopyButton value={m.password} />
                                          </span>
                                        </td>
                                        <td style={{ ...cellStyle, fontFamily: 'monospace', fontSize: '11px' }}>
                                          <span style={{ display: 'inline-flex', alignItems: 'center', gap: '4px' }}>
                                            {m.totp_secret}
                                            <CopyButton value={m.totp_secret} />
                                            <CopyOTPButton secret={m.totp_secret} />
                                          </span>
                                        </td>
                                        <td style={{ ...cellStyle, fontFamily: 'monospace', fontSize: '11px', maxWidth: '250px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                                          {m.proxy ? (
                                            <span style={{ display: 'inline-flex', alignItems: 'center', gap: '4px' }}>
                                              {m.proxy}
                                              <CopyButton value={m.proxy} />
                                            </span>
                                          ) : '-'}
                                        </td>
                                        <td style={{ ...cellStyle, fontSize: '12px', fontFamily: 'monospace' }}>{m.port || '-'}</td>
                                        <td style={cellStyle}><StatusBadge status={m.status} /></td>
                                        <td style={{ ...cellStyle, fontSize: '11px', color: 'var(--text-secondary)', maxWidth: '200px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                                          {m.message || '-'}
                                        </td>
                                      </tr>
                                    ))}
                                  </tbody>
                                </table>
                              ) : (
                                <div style={{ padding: '12px 40px', color: 'var(--text-tertiary)', fontSize: '13px' }}>No members in this run</div>
                              )}
                            </div>
                          ) : (
                            <div style={{ padding: '12px 40px', color: 'var(--text-tertiary)', fontSize: '13px' }}>No data</div>
                          )}
                        </td>
                      </tr>
                    )}
                  </>
                ))}
              </tbody>
            </table>
            {groupRuns.length === 0 && (
              <div style={{ textAlign: 'center', padding: '40px 0', color: 'var(--text-tertiary)' }}>No group runs found</div>
            )}
          </div>
        )}

        {/* Pagination */}
        <div style={paginationStyle}>
          <span>{currentTotal} total</span>
          <div style={{ display: 'flex', gap: '8px', alignItems: 'center' }}>
            <Button size="sm" variant="secondary" disabled={page <= 1} onClick={() => setPage(page - 1)}>Prev</Button>
            <span>{page} / {totalPages}</span>
            <Button size="sm" variant="secondary" disabled={page >= totalPages} onClick={() => setPage(page + 1)}>Next</Button>
          </div>
        </div>
      </Card>

      <BatchImportModal
        open={batchModalOpen}
        onClose={() => setBatchModalOpen(false)}
        onImport={handleBatchImport}
        title={`Batch Import ${batchModalType === 'proxy' ? 'Proxies' : batchModalType === 'leader' ? 'Leaders' : 'Members'}`}
        placeholder={batchPlaceholder}
      />
    </div>
  );
}
