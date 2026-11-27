import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useUnsavedChangesGuard } from '@/hooks/useUnsavedChangesGuard';
import { useNotificationStore } from '@/stores';
import { IconCheckCircle2, IconExternalLink, IconLoader2, IconPlus } from '@/components/ui/icons';
import { APIKEY_FUN_AFFILIATE_URL, APIKEY_FUN_DASHBOARD_URL } from '../sponsor';
import { allSponsorProtocolsBlocked } from '../sponsorBundle';
import { isSponsorPartialMutationError } from '../sponsorMutationRecovery';
import type { ProviderEntryFormInput, ProviderResource } from '../types';
import type { UseProviderWorkbenchResult } from '../useProviderWorkbench';
import { SponsorProviderForm } from '../sheets/forms/SponsorProviderForm';
import formStyles from '../sheets/forms/sharedForm.module.scss';
import { BrandLogo } from './BrandLogo';
import styles from './SponsorQuickStartPanel.module.scss';

interface SponsorQuickStartPanelProps {
  resource: ProviderResource | null;
  workbench: UseProviderWorkbenchResult;
  mutationDisabled?: boolean;
}

export function SponsorQuickStartPanel({
  resource,
  workbench,
  mutationDisabled = false,
}: SponsorQuickStartPanelProps) {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const formId = useId();
  const titleId = useId();
  const [submitting, setSubmitting] = useState(false);
  const [isDirty, setIsDirty] = useState(false);
  const [formVersion, setFormVersion] = useState(0);
  const [showCreateForm, setShowCreateForm] = useState(false);

  const formMutating = submitting || mutationDisabled || workbench.mutating;
  const mode = resource ? 'edit' : 'create';
  const ambiguousProtocols = workbench.snapshot?.groups.find(
    (group) => group.id === 'apikeyFun'
  )?.ambiguousProtocols;
  const sponsorBlocked = allSponsorProtocolsBlocked('apikeyFun', ambiguousProtocols);
  const submitDisabled = sponsorBlocked || formMutating || (mode === 'edit' && !isDirty);

  useUnsavedChangesGuard({
    shouldBlock: isDirty && !submitting,
    dialog: {
      title: t('providersPage.unsavedChanges.title'),
      message: t('providersPage.unsavedChanges.message'),
      confirmText: t('providersPage.unsavedChanges.discard'),
      cancelText: t('providersPage.unsavedChanges.keepEditing'),
      variant: 'danger',
    },
  });

  const handleSubmit = async (input: ProviderEntryFormInput) => {
    if (mutationDisabled) return;
    setSubmitting(true);
    try {
      if (resource) {
        await workbench.updateProvider(resource, input);
        showNotification(t('providersPage.toast.updated'), 'success');
      } else {
        await workbench.createProvider('apikeyFun', input);
        showNotification(t('providersPage.toast.created'), 'success');
        setShowCreateForm(false);
      }
      setIsDirty(false);
      setFormVersion((current) => current + 1);
    } catch (err) {
      if (isSponsorPartialMutationError(err)) {
        showNotification(t('providersPage.sponsor.partialMutationWarning'), 'warning');
        throw err;
      }
      const msg = err instanceof Error ? err.message : String(err);
      showNotification(
        `${t(resource ? 'notification.update_failed' : 'notification.add_failed')}: ${msg}`,
        'error'
      );
      throw err;
    } finally {
      setSubmitting(false);
    }
  };

  if (!resource && !showCreateForm) {
    return (
      <section className={styles.panel} aria-labelledby={titleId}>
        <div className={styles.empty}>
          <BrandLogo brand="apikeyFun" size={48} />
          <h2 id={titleId} className={styles.emptyTitle}>
            {t('providersPage.providerNames.apikeyFun')}
          </h2>
          <span className={styles.emptyText}>{t('providersPage.sponsor.emptyRegisterHint')}</span>
          <div className={styles.emptyActions}>
            <button
              type="button"
              className={`${formStyles.footerBtn} ${formStyles.footerBtnPrimary}`}
              onClick={() => setShowCreateForm(true)}
              disabled={formMutating}
            >
              <IconPlus size={15} />
              <span>{t('providersPage.actions.new')}</span>
            </button>
            <a
              className={styles.linkPill}
              href={APIKEY_FUN_AFFILIATE_URL}
              target="_blank"
              rel="noreferrer"
            >
              <span>{t('providersPage.sponsor.registerNow')}</span>
              <IconExternalLink size={13} aria-hidden="true" />
            </a>
          </div>
        </div>
      </section>
    );
  }

  const actionHref = resource ? APIKEY_FUN_DASHBOARD_URL : APIKEY_FUN_AFFILIATE_URL;
  const actionLabel = resource
    ? t('providersPage.sponsor.dashboardLink')
    : t('providersPage.sponsor.registerLink');

  return (
    <section className={styles.panel} aria-labelledby={titleId}>
      <div className={styles.header}>
        <BrandLogo brand="apikeyFun" size={32} />
        <h2 id={titleId} className={styles.title}>
          {t('providersPage.providerNames.apikeyFun')}
        </h2>
        <a className={styles.linkPill} href={actionHref} target="_blank" rel="noreferrer">
          <span>{actionLabel}</span>
          <IconExternalLink size={13} aria-hidden="true" />
        </a>
      </div>

      <div className={styles.body}>
        <SponsorProviderForm
          ambiguousProtocols={ambiguousProtocols}
          key={`${mode}:${resource?.id ?? 'new'}:${formVersion}`}
          resource={resource}
          mode={mode}
          mutating={formMutating}
          formId={formId}
          variant="quickStart"
          onSubmit={handleSubmit}
          onDirtyChange={setIsDirty}
        />
      </div>

      <div className={styles.footer}>
        {!resource ? (
          <button
            type="button"
            className={`${formStyles.footerBtn} ${formStyles.footerBtnGhost}`}
            onClick={() => {
              setShowCreateForm(false);
              setIsDirty(false);
              setFormVersion((current) => current + 1);
            }}
            disabled={submitting}
          >
            {t('providersPage.actions.cancel')}
          </button>
        ) : null}
        <button
          type="submit"
          form={formId}
          className={`${formStyles.footerBtn} ${formStyles.footerBtnPrimary}`}
          disabled={submitDisabled}
          aria-busy={submitting || undefined}
        >
          {submitting ? (
            <IconLoader2 className={formStyles.spin} size={14} />
          ) : mode === 'create' ? (
            <IconPlus size={14} />
          ) : (
            <IconCheckCircle2 size={14} />
          )}
          <span>
            {mode === 'create'
              ? t('providersPage.actions.create')
              : t('providersPage.actions.save')}
          </span>
        </button>
      </div>
    </section>
  );
}
