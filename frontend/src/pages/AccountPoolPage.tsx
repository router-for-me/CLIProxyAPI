import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
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
import { useGroupsData } from '@/features/accountPool/hooks/useGroupsData';
import { accountPoolApi } from '@/services/api/accountPool';
import { useNotificationStore } from '@/stores/useNotificationStore';

type TabKey = 'members' | 'leaders' | 'proxies' | 'groups';

const ACCOUNT_STATUSES = ['', 'available', 'used', 'banned', 'region-unmatch', 'auth-error', 'oauth-success', 'credential'];
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
  const { groups, total: groupsTotal, loading: groupsLoading, loadGroups } = useGroupsData();

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
      case 'groups':
        loadGroups({ leader_email: search || undefined, limit: pageSize, offset });
        break;
    }
  }, [activeTab, statusFilter, typeFilter, search, page, pageSize, loadMembers, loadLeaders, loadProxies, loadGroups]);

  useEffect(() => { reload(); }, [reload]);
  useHeaderRefresh(reload);

  useEffect(() => { setPage(1); setStatusFilter(''); setSearch(''); setTypeFilter(''); }, [activeTab]);

  const currentTotal = activeTab === 'members' ? membersTotal : activeTab === 'leaders' ? leadersTotal : activeTab === 'proxies' ? proxiesTotal : groupsTotal;
  const totalPages = Math.max(1, Math.ceil(currentTotal / pageSize));
  const loading = activeTab === 'members' ? membersLoading : activeTab === 'leaders' ? leadersLoading : activeTab === 'proxies' ? proxiesLoading : groupsLoading;

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

  const handleDeleteGroup = async (id: number) => {
    try {
      await accountPoolApi.deleteGroup(id);
      showNotification(t('account_pool.deleted', { defaultValue: 'Deleted' }), 'success');
      reload();
    } catch (err) {
      showNotification((err as Error).message, 'error');
    }
  };

  const handleUpdateGroupStatus = async (id: number, familyStatus: string) => {
    try {
      await accountPoolApi.updateGroup(id, { family_status: familyStatus });
      showNotification(t('account_pool.updated', { defaultValue: 'Updated' }), 'success');
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
        {(['members', 'leaders', 'proxies', 'groups'] as TabKey[]).map((tab) => (
          <button key={tab} style={activeTab === tab ? activeTabStyle : tabStyle} onClick={() => setActiveTab(tab)}>
            {t(`account_pool.tab_${tab}`, { defaultValue: tab.charAt(0).toUpperCase() + tab.slice(1) })}
            <span style={{ marginLeft: '6px', fontSize: '11px', opacity: 0.6 }}>
              ({tab === 'members' ? membersTotal : tab === 'leaders' ? leadersTotal : tab === 'proxies' ? proxiesTotal : groupsTotal})
            </span>
          </button>
        ))}
      </div>

      <Card>
        {/* Toolbar */}
        <div style={toolbarStyle}>
          {(activeTab === 'members' || activeTab === 'leaders' || activeTab === 'groups') && (
            <Input
              value={search}
              onChange={(e) => { setSearch(e.target.value); setPage(1); }}
              placeholder={activeTab === 'groups' ? 'Search leader email...' : 'Search email...'}
              style={{ width: '200px' }}
            />
          )}
          {(activeTab === 'members' || activeTab === 'leaders' || activeTab === 'proxies') && (
            <Select
              value={statusFilter}
              onChange={(v) => { setStatusFilter(v); setPage(1); }}
              options={ACCOUNT_STATUSES.map((s) => ({ value: s, label: s || 'All statuses' }))}
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

        {/* Groups tab */}
        {activeTab === 'groups' && !loading && (
          <div style={{ overflowX: 'auto' }}>
            <table style={{ width: '100%', borderCollapse: 'collapse' }}>
              <thead>
                <tr style={{ backgroundColor: 'var(--bg-secondary)' }}>
                  <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left', maxWidth: '60px' }}>ID</th>
                  <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>Group ID</th>
                  <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>Date</th>
                  <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>Leader</th>
                  <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>Member</th>
                  <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'left' }}>Family Status</th>
                  <th style={{ ...cellStyle, fontWeight: 600, textAlign: 'right' }}>Actions</th>
                </tr>
              </thead>
              <tbody>
                {groups.map((g) => (
                  <tr key={g.id}>
                    <td style={{ ...cellStyle, maxWidth: '60px', color: 'var(--text-tertiary)', fontSize: '12px' }}>{g.id}</td>
                    <td style={{ ...cellStyle, fontFamily: 'monospace', fontSize: '12px' }}>{g.group_id}</td>
                    <td style={cellStyle}>{g.date}</td>
                    <td style={{ ...cellStyle, fontSize: '12px' }}>
                      <span style={{ display: 'inline-flex', alignItems: 'center', gap: '4px' }}>
                        {g.leader_email}
                        <CopyButton value={g.leader_email} />
                      </span>
                    </td>
                    <td style={{ ...cellStyle, fontSize: '12px' }}>
                      {g.member_email ? (
                        <span style={{ display: 'inline-flex', alignItems: 'center', gap: '4px' }}>
                          {g.member_email}
                          <CopyButton value={g.member_email} />
                        </span>
                      ) : '-'}
                    </td>
                    <td style={cellStyle}>
                      <Input
                        defaultValue={g.family_status}
                        onBlur={(e) => {
                          if (e.target.value !== g.family_status) {
                            handleUpdateGroupStatus(g.id, e.target.value);
                          }
                        }}
                        style={{ width: '120px', fontSize: '12px', padding: '2px 6px' }}
                      />
                    </td>
                    <td style={{ ...cellStyle, textAlign: 'right' }}>
                      <Button size="sm" variant="danger" onClick={() => handleDeleteGroup(g.id)}>
                        Delete
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {groups.length === 0 && (
              <div style={{ textAlign: 'center', padding: '40px 0', color: 'var(--text-tertiary)' }}>No groups found</div>
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
