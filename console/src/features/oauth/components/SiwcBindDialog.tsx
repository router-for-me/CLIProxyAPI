import { useEffect, useId, useRef, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Modal } from '@/components/ui/Modal';
import { IconExternalLink, IconLoader2 } from '@/components/ui/icons';
import iconCodex from '@/assets/icons/codex.svg';
import { authFilesApi } from '@/services/api/authFiles';
import { captureSiwcBindingApi, oauthApi, type SiwcBindResponse } from '@/services/api/oauth';
import { useAuthStore } from '@/stores/useAuthStore';
import type { AuthFileItem } from '@/types';
import { formatDateTimeValue } from '@/utils/format';
import { notifyAuthFilesChanged } from '@/features/authFiles/authFilesEvents';
import { deriveAuthFileIdentity, stripJsonExtension } from '@/features/authFiles/identity';
import { createOAuthAttempts, type OAuthAttempt } from '../oauthAttempts';
import { splitAuthUrl } from '../flowView';
import {
  createSiwcStart,
  getSiwcBindingStatus,
  isSiwcCallbackValid,
  listSiwcTargets,
} from '../siwcBinding';
import { CopyButton } from './CopyButton';
import { DialogTitle } from './OAuthFlowDialog';
import { SiwcStatusBadge } from './SiwcStatusBadge';
import dialogStyles from './OAuthFlowDialog.module.scss';
import styles from './SiwcBindDialog.module.scss';

type Phase = 'idle' | 'starting' | 'wait' | 'ok' | 'error' | 'unsupported' | 'expired';
type Feedback = 'submitted' | 'invalid_callback' | 'callback_failed';

const CALLBACK_PLACEHOLDER = 'http://127.0.0.1:1455/auth/callback?code=…&state=…';

export interface SiwcBindDialogProps {
  open: boolean;
  onClose: () => void;
  onViewAuthFiles: () => void;
}

/**
 * SIWC 绑定：与 OAuth 授权对话框同一套语汇（品牌头 / 步骤 / 底部状态条 / 成功视图）。
 * 先在对话框内选择 Codex 凭证，再开始授权；授权开始后凭证锁定，失败可原地重来。
 * 每次打开由父级以新 key 重新挂载，关闭时取消仍在进行的服务端会话。
 */
export function SiwcBindDialog({ open, onClose, onViewAuthFiles }: SiwcBindDialogProps) {
  const { t, i18n } = useTranslation();
  const [targets, setTargets] = useState<AuthFileItem[] | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [name, setName] = useState('');
  const [run, setRun] = useState(0);
  const [session, setSession] = useState<SiwcBindResponse>();
  const [phase, setPhase] = useState<Phase>('idle');
  const [callback, setCallback] = useState('');
  const [feedback, setFeedback] = useState<Feedback | ''>('');
  const [submitting, setSubmitting] = useState(false);
  const attemptRef = useRef<OAuthAttempt | null>(null);
  const startRef = useRef<ReturnType<typeof createSiwcStart<SiwcBindResponse>> | null>(null);
  const closeRef = useRef(onClose);
  useEffect(() => {
    closeRef.current = onClose;
  }, [onClose]);

  // 连接守卫 + 凭证列表：切换连接或登出时立即作废并关闭，旧响应不得写入。
  useEffect(() => {
    let active = true;
    const connection = useAuthStore.getState();
    const sameConnection = () => {
      const current = useAuthStore.getState();
      return (
        current.isAuthenticated === connection.isAuthenticated &&
        current.apiBase === connection.apiBase &&
        current.managementKey === connection.managementKey
      );
    };
    const unsubscribe = useAuthStore.subscribe(() => {
      if (!sameConnection()) {
        active = false;
        attemptRef.current?.invalidate();
        startRef.current?.close();
        closeRef.current();
      }
    });
    authFilesApi
      .list()
      .then((response) => {
        if (!active || !sameConnection()) return;
        const list = listSiwcTargets(response.files);
        setTargets(list);
        if (list.length === 1) setName(list[0].name);
      })
      .catch(() => {
        if (active) setLoadFailed(true);
      });
    return () => {
      active = false;
      unsubscribe();
    };
  }, []);

  useEffect(() => {
    if (!name) return;
    const attempts = createOAuthAttempts({
      setTimeout: (fn, delay) => window.setTimeout(fn, delay),
      clearTimeout: (timer) => window.clearTimeout(timer),
    });
    const attempt = attempts.begin(name);
    attemptRef.current = attempt;
    const api = captureSiwcBindingApi();
    const startup = createSiwcStart({
      start: () => {
        setPhase('starting');
        return api.start(name);
      },
      cancel: api.cancel,
      onResult: (result) => {
        if (!attempt.isCurrent()) return;
        const state = result.state;
        const url = new URL(result.url);
        if (
          result.status !== 'ok' ||
          result.flow !== 'siwc_bind' ||
          !state ||
          !['https:', 'http:'].includes(url.protocol) ||
          !(result.expires_in > 0)
        ) {
          throw new Error('Invalid binding session');
        }
        setSession(result);
        setPhase('wait');
        const expiresAt = Date.now() + result.expires_in * 1000;
        attempt.poll<{ status: 'wait' | 'ok' | 'error' | 'expired' }>(
          () =>
            Date.now() >= expiresAt
              ? Promise.resolve({ status: 'expired' as const })
              : oauthApi.getAuthStatus(state, attempt.signal),
          (result) => {
            setPhase(result.status);
            if (result.status === 'ok') {
              startup.complete();
              notifyAuthFilesChanged();
            }
            return result.status === 'wait';
          },
          () => setPhase('error'),
          2000
        );
      },
      onError: (error: unknown) => {
        if (!attempt.isCurrent()) return;
        setPhase(
          typeof error === 'object' && error !== null && 'status' in error && error.status === 404
            ? 'unsupported'
            : 'error'
        );
      },
    });
    startRef.current = startup;
    return () => {
      attempt.invalidate();
      startup.close();
      if (attemptRef.current === attempt) attemptRef.current = null;
      if (startRef.current === startup) startRef.current = null;
    };
  }, [name, run]);

  const close = () => {
    attemptRef.current?.invalidate();
    startRef.current?.close();
    onClose();
  };

  /** 失败后原地重来：丢弃旧尝试（effect 清理会取消服务端会话），回到选择视图。 */
  const restart = () => {
    setSession(undefined);
    setCallback('');
    setFeedback('');
    setSubmitting(false);
    setPhase('idle');
    setRun((value) => value + 1);
  };

  const submit = async () => {
    const attempt = attemptRef.current;
    if (!session || !attempt?.isCurrent() || phase !== 'wait' || submitting) return;
    if (!isSiwcCallbackValid(callback, session.state)) {
      setFeedback('invalid_callback');
      return;
    }
    setSubmitting(true);
    setFeedback('');
    try {
      await oauthApi.submitCallback('codex', callback, attempt.signal);
      if (attempt.isCurrent()) setFeedback('submitted');
    } catch {
      if (attempt.isCurrent()) setFeedback('callback_failed');
    } finally {
      if (attempt.isCurrent()) setSubmitting(false);
    }
  };

  const selected = targets?.find((file) => file.name === name);
  const expiresOf = (file: AuthFileItem) =>
    file.siwcExpiresAt ? formatDateTimeValue(file.siwcExpiresAt, i18n.language) : '';
  const view =
    phase === 'idle'
      ? 'pick'
      : phase === 'ok'
        ? 'success'
        : phase === 'starting' || phase === 'wait'
          ? 'flow'
          : 'failed';

  return (
    <Modal
      open={open}
      onClose={close}
      width={500}
      className={dialogStyles.dialog}
      title={
        <DialogTitle
          title={t('siwc.title')}
          caption={`Codex · ${t('auth_login.method_browser')}`}
          icon={iconCodex}
        />
      }
      footer={
        view === 'flow' ? (
          <FlowStatusBar
            generating={phase === 'starting'}
            onCancel={close}
            label={t(phase === 'starting' ? 'siwc.starting' : 'siwc.wait')}
            cancelLabel={t('siwc.cancel')}
          />
        ) : undefined
      }
    >
      <div className={dialogStyles.view} key={view}>
        {view === 'pick' && (
          <div className={dialogStyles.stack}>
            <p className={dialogStyles.hint}>{t('siwc.intro')}</p>
            <TargetPicker
              targets={targets}
              loadFailed={loadFailed}
              value={name}
              onChange={setName}
              expiresOf={expiresOf}
            />
            <div>
              <button
                type="button"
                className={dialogStyles.primary}
                disabled={!name}
                onClick={() => void startRef.current?.start()}
              >
                {t('siwc.start')}
              </button>
            </div>
          </div>
        )}

        {view === 'flow' && (
          <div className={dialogStyles.stack}>
            {selected && <TargetSummary file={selected} />}
            <ol className={dialogStyles.steps}>
              <OpenStep url={session?.url} />
              <CallbackStep
                disabled={!session || phase !== 'wait'}
                value={callback}
                submitting={submitting}
                feedback={feedback}
                onChange={(value) => {
                  setCallback(value);
                  if (feedback && feedback !== 'submitted') setFeedback('');
                }}
                onSubmit={() => void submit()}
              />
            </ol>
          </div>
        )}

        {view === 'failed' && (
          <div className={dialogStyles.stack}>
            {selected && <TargetSummary file={selected} />}
            <div className={dialogStyles.alert} role="alert">
              {t(`siwc.${phase}`)}
            </div>
            {phase !== 'unsupported' && (
              <div>
                <button type="button" className={dialogStyles.primary} onClick={restart}>
                  {t('siwc.retry')}
                </button>
              </div>
            )}
          </div>
        )}

        {view === 'success' && (
          <div className={dialogStyles.result} role="status">
            <svg className={dialogStyles.successMark} viewBox="0 0 52 52" aria-hidden="true">
              <circle className={dialogStyles.successRing} cx="26" cy="26" r="24" />
              <path className={dialogStyles.successCheck} d="M16 27l7 7 14-15" />
            </svg>
            <div className={dialogStyles.resultTitle}>{t('siwc.ok')}</div>
            {selected && (
              <p className={dialogStyles.resultHint}>
                {t('siwc.ok_hint', { name: deriveAuthFileIdentity(selected).primary })}
              </p>
            )}
            <div className={dialogStyles.resultActions}>
              <button type="button" className={dialogStyles.secondary} onClick={onViewAuthFiles}>
                {t('auth_login.view_auth_files')}
              </button>
              <button type="button" className={dialogStyles.primary} onClick={close}>
                {t('siwc.done')}
              </button>
            </div>
          </div>
        )}
      </div>
    </Modal>
  );
}

/* ---------- 凭证选择：原生 radio（键盘/读屏零成本），视觉为可点行 ---------- */

function TargetPicker({
  targets,
  loadFailed,
  value,
  onChange,
  expiresOf,
}: {
  targets: AuthFileItem[] | null;
  loadFailed: boolean;
  value: string;
  onChange: (name: string) => void;
  expiresOf: (file: AuthFileItem) => string;
}) {
  const { t } = useTranslation();
  const groupName = useId();

  let content: ReactNode;
  if (loadFailed) {
    content = (
      <div className={dialogStyles.alert} role="alert">
        {t('siwc.load_failed')}
      </div>
    );
  } else if (!targets) {
    content = (
      <div className={styles.options} aria-busy="true" aria-label={t('siwc.loading')}>
        <span className={styles.skeletonRow} />
        <span className={styles.skeletonRow} />
      </div>
    );
  } else if (targets.length === 0) {
    content = <p className={styles.empty}>{t('siwc.no_targets')}</p>;
  } else {
    content = (
      <div className={styles.options}>
        {targets.map((file) => {
          const identity = deriveAuthFileIdentity(file);
          const expires = expiresOf(file);
          const meta = [expires && t('siwc.expires', { date: expires }), identity.secondary]
            .filter(Boolean)
            .join(' · ');
          return (
            <label
              key={file.name}
              className={styles.option}
              data-checked={value === file.name || undefined}
            >
              <input
                type="radio"
                className={styles.radioInput}
                name={groupName}
                value={file.name}
                checked={value === file.name}
                onChange={() => onChange(file.name)}
              />
              <span className={styles.radio} aria-hidden="true" />
              <span className={styles.optionText}>
                <span
                  className={`${styles.optionName} ${
                    identity.kind === 'fileName' ? styles.optionNameMono : ''
                  }`}
                  title={identity.fullName}
                >
                  {identity.primary || stripJsonExtension(file.name)}
                </span>
                {meta && <span className={styles.optionMeta}>{meta}</span>}
              </span>
              <SiwcStatusBadge status={getSiwcBindingStatus(file)} withLabel={false} />
            </label>
          );
        })}
      </div>
    );
  }

  return (
    <fieldset className={styles.picker}>
      <legend className={styles.legend}>{t('siwc.target')}</legend>
      {content}
    </fieldset>
  );
}

/** 授权开始后的锁定目标：单行摘要，不再可改。 */
function TargetSummary({ file }: { file: AuthFileItem }) {
  const { t } = useTranslation();
  const identity = deriveAuthFileIdentity(file);
  return (
    <div className={styles.summary}>
      <span className={styles.summaryLabel}>{t('siwc.target')}</span>
      <span
        className={`${styles.summaryName} ${identity.kind === 'fileName' ? styles.optionNameMono : ''}`}
        title={identity.fullName}
      >
        {identity.primary || stripJsonExtension(file.name)}
      </span>
      <SiwcStatusBadge status={getSiwcBindingStatus(file)} withLabel={false} />
    </div>
  );
}

/* ---------- 步骤：与 OAuthFlowDialog 同构 ---------- */

function Step({
  title,
  titleFor,
  children,
}: {
  title: string;
  titleFor?: string;
  children: ReactNode;
}) {
  return (
    <li className={dialogStyles.step}>
      <span className={dialogStyles.stepIndex} aria-hidden="true" />
      <div className={dialogStyles.stepBody}>
        <div className={dialogStyles.stepHead}>
          {titleFor ? (
            <label className={dialogStyles.stepTitle} htmlFor={titleFor}>
              {title}
            </label>
          ) : (
            <span className={dialogStyles.stepTitle}>{title}</span>
          )}
        </div>
        {children}
      </div>
    </li>
  );
}

function OpenStep({ url }: { url?: string }) {
  const { t } = useTranslation();
  const parts = url ? splitAuthUrl(url) : null;
  return (
    <Step title={t('siwc.step_open')}>
      <div className={dialogStyles.urlField} aria-busy={!url}>
        {parts ? (
          <span className={dialogStyles.urlText} title={url}>
            <span className={dialogStyles.urlHost}>{parts.host}</span>
            <span className={dialogStyles.urlRest}>{parts.rest}</span>
          </span>
        ) : (
          <span className={dialogStyles.urlSkeleton} aria-label={t('siwc.starting')} />
        )}
        {url && <CopyButton value={url} label={t('siwc.copy')} />}
      </div>
      <div>
        <button
          type="button"
          className={dialogStyles.primary}
          disabled={!url}
          onClick={() => url && window.open(url, '_blank', 'noopener,noreferrer')}
        >
          {t('siwc.open')}
          <IconExternalLink size={14} aria-hidden="true" />
        </button>
      </div>
    </Step>
  );
}

function CallbackStep({
  disabled,
  value,
  submitting,
  feedback,
  onChange,
  onSubmit,
}: {
  disabled: boolean;
  value: string;
  submitting: boolean;
  feedback: Feedback | '';
  onChange: (value: string) => void;
  onSubmit: () => void;
}) {
  const { t } = useTranslation();
  const inputId = useId();
  const hintId = useId();
  const feedbackId = useId();
  const isError = feedback === 'invalid_callback' || feedback === 'callback_failed';

  return (
    <Step title={t('siwc.callback')} titleFor={inputId}>
      <p className={dialogStyles.stepHint} id={hintId}>
        {t('siwc.hint')}
      </p>
      <form
        className={dialogStyles.callbackRow}
        onSubmit={(event) => {
          event.preventDefault();
          onSubmit();
        }}
      >
        <input
          id={inputId}
          className={`input ${dialogStyles.callbackInput}`}
          value={value}
          onChange={(event) => onChange(event.target.value)}
          placeholder={CALLBACK_PLACEHOLDER}
          disabled={disabled}
          autoComplete="off"
          spellCheck={false}
          aria-invalid={isError}
          aria-describedby={feedback ? `${feedbackId} ${hintId}` : hintId}
        />
        <button
          type="submit"
          className={dialogStyles.secondary}
          disabled={disabled || submitting || !value.trim()}
        >
          {submitting && (
            <IconLoader2 size={14} className={dialogStyles.spinning} aria-hidden="true" />
          )}
          {t('siwc.submit')}
        </button>
      </form>
      {feedback && (
        <p
          id={feedbackId}
          className={isError ? dialogStyles.feedbackError : dialogStyles.feedbackSuccess}
          role={isError ? 'alert' : 'status'}
        >
          {t(`siwc.${feedback}`)}
        </p>
      )}
    </Step>
  );
}

function FlowStatusBar({
  generating,
  label,
  cancelLabel,
  onCancel,
}: {
  generating: boolean;
  label: string;
  cancelLabel: string;
  onCancel: () => void;
}) {
  return (
    <div className={dialogStyles.statusBar} data-tone="waiting">
      <p className={dialogStyles.statusText} role="status" aria-live="polite">
        {generating ? (
          <IconLoader2 size={13} className={dialogStyles.spinning} aria-hidden="true" />
        ) : (
          <span className={dialogStyles.statusDot} aria-hidden="true" />
        )}
        <span className={dialogStyles.statusLabel}>{label}</span>
      </p>
      <button type="button" className={dialogStyles.ghost} onClick={onCancel}>
        {cancelLabel}
      </button>
    </div>
  );
}
