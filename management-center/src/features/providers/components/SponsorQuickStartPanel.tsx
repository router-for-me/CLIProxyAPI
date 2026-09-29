import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useUnsavedChangesGuard } from '@/hooks/useUnsavedChangesGuard';
import { useNotificationStore } from '@/stores';
import { IconCheckCircle2, IconExternalLink, IconLoader2, IconPlus } from '@/components/ui/icons';
import { PROVIDER_LOGOS } from '../brandLogos';
import { APIKEY_FUN_AFFILIATE_URL, APIKEY_FUN_DASHBOARD_URL } from '../sponsor';
import { isSponsorPartialMutationError } from '../sponsorMutationRecovery';
import type { ProviderEntryFormInput, ProviderResource } from '../types';
import type { UseProviderWorkbenchResult } from '../useProviderWorkbench';
import { SponsorProviderForm } from '../sheets/forms/SponsorProviderForm';
import formStyles from '../sheets/forms/sharedForm.module.scss';
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
  const [submitting, setSubmitting] = useState(false);
  const [isDirty, setIsDirty] = useState(false);
  const [formVersion, setFormVersion] = useState(0);
  const [showCreateForm, setShowCreateForm] = useState(false);

  const formMutating = submitting || mutationDisabled || workbench.mutating;
  const mode = resource ? 'edit' : 'create';
  const submitDisabled = formMutating || (mode === 'edit' && !isDirty);
  const logo = PROVIDER_LOGOS.apikeyFun;

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
      <section className={styles.panel}>
        <div className={styles.header}>
          <div className={styles.titleRow}>
            <img src={logo.src} alt="" aria-hidden="true" className={styles.logo} />
            <div className={styles.titleText}>
              <h2 className={styles.title}>{t('providersPage.providerNames.apikeyFun')}</h2>
            </div>
          </div>
        </div>

        <div className={styles.empty}>
          <div>{t('providersPage.sponsor.emptyRegisterHint')}</div>
          <div className={styles.emptyActions}>
            <button
              type="button"
              className={`${styles.emptyActionButton} ${styles.emptyActionButtonPrimary}`}
              onClick={() => setShowCreateForm(true)}
              disabled={formMutating}
            >
              <IconPlus size={16} />
              <span>{t('providersPage.actions.new')}</span>
            </button>
            <a
              className={`${styles.emptyActionButton} ${styles.emptyActionButtonEmphasis}`}
              href={APIKEY_FUN_AFFILIATE_URL}
              target="_blank"
              rel="noreferrer"
            >
              <IconExternalLink size={16} />
              <span>{t('providersPage.sponsor.registerNow')}</span>
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
    <section className={styles.panel}>
      <div className={styles.header}>
        <div className={styles.titleRow}>
          <img src={logo.src} alt="" aria-hidden="true" className={styles.logo} />
          <div className={styles.titleText}>
            <h2 className={styles.title}>{t('providersPage.providerNames.apikeyFun')}</h2>
          </div>
          <a className={styles.topLink} href={actionHref} target="_blank" rel="noreferrer">
            <IconExternalLink size={14} />
            <span>{actionLabel}</span>
          </a>
        </div>
      </div>

      <SponsorProviderForm
        key={`${mode}:${resource?.id ?? 'new'}:${formVersion}`}
        resource={resource}
        mode={mode}
        mutating={formMutating}
        formId={formId}
        variant="quickStart"
        onSubmit={handleSubmit}
        onDirtyChange={setIsDirty}
      />

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
          className={`${formStyles.footerBtn} ${formStyles.footerBtnPrimary} ${
            styles.primaryAction
          }`}
          disabled={submitDisabled}
        >
          {submitting ? (
            <IconLoader2 className={styles.spin} size={14} />
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
