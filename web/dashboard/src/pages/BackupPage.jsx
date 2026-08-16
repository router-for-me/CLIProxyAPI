import React, { useEffect, useState } from 'react';
import {
  listBackups, createBackup, restoreBackup, backupSettings,
} from '../api/client.js';
import { ErrorBanner, EmptyState } from '../components/Primitives.jsx';
import { useToast } from '../components/Toast.jsx';

// formatTime renders an ISO timestamp compactly for the snapshot list.
function formatTime(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString();
}

// formatBytes renders a byte count as a human-friendly size.
function formatBytes(n) {
  if (!n && n !== 0) return '—';
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

// BackupPage — full backup to S3: trigger a manual snapshot, list stored
// snapshots, and restore one with replace/merge modes. The page is driven by
// the /v0/management/backup endpoints and shows 503 guidance when S3 backup
// is not configured.
export default function BackupPage() {
  const [snapshots, setSnapshots] = useState([]);
  const [settings, setSettings] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);
  const [backingUp, setBackingUp] = useState(false);
  const [restoringKey, setRestoringKey] = useState(null); // snapshot key currently restoring
  const [confirming, setConfirming] = useState(null); // { key } when the replace-confirm dialog is open
  const toast = useToast();

  const load = async () => {
    try {
      const [listResp, settingsResp] = await Promise.all([listBackups(), backupSettings()]);
      setSnapshots(listResp?.snapshots || []);
      setSettings(settingsResp?.settings || null);
      setError(null);
    } catch (e) {
      // 503 → S3 backup not configured; surface a friendly message.
      if (e.status === 503) {
        setError({ configured: false, message: e.message });
      } else {
        setError({ configured: true, message: e.message });
      }
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { load(); }, []);

  async function handleBackupNow() {
    setError(null);
    setBackingUp(true);
    try {
      const resp = await createBackup();
      toast.success(`Backup created: ${resp?.snapshot?.key || 'snapshot'}`);
      await load();
    } catch (e) {
      setError({ configured: true, message: e.message });
    } finally {
      setBackingUp(false);
    }
  }

  async function handleRestore(snapshot, mode) {
    setError(null);
    setRestoringKey(snapshot.key);
    try {
      const resp = await restoreBackup({
        objectKey: snapshot.key,
        mode,
        confirm: mode === 'replace',
      });
      const result = resp?.restore;
      const report = result?.report;
      const inserted = report?.total_inserted || 0;
      const files = result?.files || 0;
      if (report?.partial) {
        toast.warn(`Restore completed with skipped chunks — ${inserted} inserted, ${files} file${files === 1 ? '' : 's'} written.`);
      } else {
        toast.success(`Restore complete — ${inserted} ${inserted === 1 ? 'row' : 'rows'} applied, ${files} file${files === 1 ? '' : 's'} written.`);
      }
      setConfirming(null);
    } catch (e) {
      setError({ configured: true, message: e.message });
    } finally {
      setRestoringKey(null);
    }
  }

  const s3Configured = settings?.s3_configured;
  const notConfigured = error && !error.configured;

  return (
    <div>
      <h1>Backups</h1>
      <p className="muted" style={{ marginTop: 4, maxWidth: 720 }}>
        Full server state (config.yaml, auths/ files, and all PostgreSQL tables) is
        snapshotted to an S3-compatible object store. Backups can run on a schedule
        or be triggered manually; snapshots can be restored with a non-destructive
        merge or a full replace.
      </p>

      <ErrorBanner error={error ? error.message : null} onRetry={notConfigured ? null : load} />

      {/* Status panel */}
      <div className="card" style={{ marginTop: 16 }}>
        <div className="row row--between">
          <h3 className="card__title" style={{ margin: 0 }}>Configuration</h3>
          <button onClick={handleBackupNow} disabled={backingUp || !s3Configured} style={{ minWidth: 130 }}>
            {backingUp ? 'Backing up…' : 'Backup now'}
          </button>
        </div>
        <div className="row gap-lg" style={{ marginTop: 12 }}>
          <div>
            <span className="dim">Status</span>
            <div>{s3Configured ? <span className="badge badge--active">Configured</span> : <span className="badge badge--disabled">Not configured</span>}</div>
          </div>
          <div>
            <span className="dim">Schedule</span>
            <div>{settings?.interval && settings.interval !== '0s' ? `Every ${settings.interval}` : 'Manual only'}</div>
          </div>
          <div>
            <span className="dim">Retention</span>
            <div>{settings?.retention ? `${settings.retention} snapshots` : 'Keep all'}</div>
          </div>
        </div>
        {!s3Configured && (
          <p className="muted" style={{ marginTop: 12, fontSize: 12 }}>
            Set <code>BACKUP_S3_ENDPOINT</code> (plus bucket/credentials) to enable full
            backup to S3. Until then the backup endpoints return 503.
          </p>
        )}
      </div>

      {/* Snapshots list */}
      <div className="card" style={{ marginTop: 16 }}>
        <h3 className="card__title" style={{ margin: 0 }}>Snapshots</h3>
        {loading ? (
          <p className="muted" style={{ marginTop: 12 }}>Loading snapshots…</p>
        ) : snapshots.length === 0 ? (
          <EmptyState
            title="No snapshots yet"
            hint="Trigger a backup to create the first snapshot."
            actions={s3Configured ? <button onClick={handleBackupNow} disabled={backingUp}>Backup now</button> : null}
          />
        ) : (
          <table className="table" style={{ marginTop: 12 }}>
            <thead>
              <tr><th>Key</th><th>Size</th><th>Exported at</th><th></th></tr>
            </thead>
            <tbody>
              {snapshots.map((s) => (
                <tr key={s.key}>
                  <td><code>{s.key}</code></td>
                  <td>{formatBytes(s.size)}</td>
                  <td>{formatTime(s.exported_at)}</td>
                  <td style={{ textAlign: 'right' }}>
                    {confirming === s.key ? (
                      <span className="row gap-sm" style={{ justifyContent: 'flex-end' }}>
                        <span className="dim" style={{ fontSize: 12 }}>Replace wipes existing data.</span>
                        <button onClick={() => handleRestore(s, 'replace')} disabled={restoringKey === s.key}>Confirm replace</button>
                        <button onClick={() => setConfirming(null)}>Cancel</button>
                      </span>
                    ) : (
                      <span className="row gap-sm" style={{ justifyContent: 'flex-end' }}>
                        <button onClick={() => handleRestore(s, 'merge')} disabled={restoringKey === s.key}>
                          {restoringKey === s.key ? 'Restoring…' : 'Merge'}
                        </button>
                        <button onClick={() => setConfirming(s.key)} disabled={restoringKey === s.key}>Replace…</button>
                      </span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
