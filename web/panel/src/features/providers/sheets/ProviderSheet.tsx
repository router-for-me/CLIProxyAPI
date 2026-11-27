import { useCallback, useEffect, useId, useImperativeHandle, useState, type Ref } from 'react';
import { useTranslation } from 'react-i18next';
import { Sheet } from '@/components/ui/Sheet';
import { IconLoader2, IconPencil } from '@/components/ui/icons';
import type { ProviderRecentUsageMap } from '@/components/providers/utils';
import { useNotificationStore } from '@/stores';
import { PROVIDER_DESCRIPTORS } from '../descriptors';
import { allSponsorProtocolsBlocked } from '../sponsorBundle';
import { isMultiProtocolSponsorBrand } from '../sponsorDefinitions';
import type {
  NativeSection,
  ProviderBrand,
  ProviderEntryFormInput,
  ProviderGroup,
  ProviderResource,
} from '../types';
import type { UseProviderWorkbenchResult } from '../useProviderWorkbench';
import { BaseProviderForm } from './forms/BaseProviderForm';
import { ResourceDetailView } from './ResourceDetailView';
import { SponsorProviderForm } from './forms/SponsorProviderForm';
import { BrandLogo } from '../components/BrandLogo';
import { ProviderPicker } from '../components/ProviderPicker';
import formStyles from './forms/sharedForm.module.scss';
import styles from './ProviderSheet.module.scss';

import { isNativeFamily } from '../nativeSections';
import type { NativeConnectionFormOutput } from '../nativeConnectionForm';

type SheetMode = 'pick' | 'detail' | 'create' | 'edit';

export interface ProviderSheetState {
  open: boolean;
  brand: ProviderBrand;
  mode: SheetMode;
  resource: ProviderResource | null;
  /** A create form opened from the picker returns to it on cancel. */
  fromPicker?: boolean;
  section?: NativeSection;
}

export interface ProviderSheetHandle {
  confirmDiscardIfDirty: () => Promise<boolean>;
}

interface ProviderSheetProps {
  state: ProviderSheetState;
  onClose: () => void;
  onSwitchToEdit: () => void;
  workbench: UseProviderWorkbenchResult;
  onCreated: () => void;
  onUpdated: () => void;
  onDeleteConnection: (section: NativeSection) => void;
  mutationDisabled?: boolean;
  usageByProvider?: ProviderRecentUsageMap;
  /** Brands offered by the "add provider" picker. */
  pickerGroups?: ProviderGroup[];
  onPick?: (brand: ProviderBrand) => void;
  onBackToPicker?: () => void;
  ref?: Ref<ProviderSheetHandle>;
}

export function ProviderSheet({
  state,
  onClose,
  onSwitchToEdit,
  workbench,
  onCreated,
  onUpdated,
  onDeleteConnection,
  mutationDisabled = false,
  usageByProvider,
  pickerGroups = [],
  onPick,
  onBackToPicker,
  ref,
}: ProviderSheetProps) {
  const { t } = useTranslation();
  const { showConfirmation } = useNotificationStore();
  const formId = useId();
  const [submitting, setSubmitting] = useState(false);
  const [isDirty, setIsDirty] = useState(false);

  // Reset dirty flag whenever the sheet is closed or the editing target
  // (brand / resource / mode) changes — the child form will re-mount and
  // re-report its own dirty state.
  useEffect(() => {
    setIsDirty(false);
  }, [state.brand, state.mode, state.resource?.id, state.section?.source.groupIndex, state.open]);

  const handleDirtyChange = useCallback((dirty: boolean) => {
    setIsDirty(dirty);
  }, []);

  const descriptor = PROVIDER_DESCRIPTORS[state.brand];
  const isEditingForm = state.mode !== 'pick' && state.mode !== 'detail';
  const formMutating = submitting || mutationDisabled;
  const ambiguousProtocols = workbench.snapshot?.groups.find(
    (group) => group.id === state.brand
  )?.ambiguousProtocols;
  const sponsorBlocked =
    isMultiProtocolSponsorBrand(state.brand) &&
    allSponsorProtocolsBlocked(state.brand, ambiguousProtocols);
  const submitDisabled = sponsorBlocked || formMutating || (state.mode === 'edit' && !isDirty);

  const confirmDiscardIfDirty = useCallback((): Promise<boolean> => {
    if (!isEditingForm || !isDirty || submitting) {
      return Promise.resolve(true);
    }
    return new Promise<boolean>((resolve) => {
      showConfirmation({
        title: t('providersPage.unsavedChanges.title'),
        message: t('providersPage.unsavedChanges.message'),
        variant: 'danger',
        confirmText: t('providersPage.unsavedChanges.discard'),
        cancelText: t('providersPage.unsavedChanges.keepEditing'),
        onConfirm: () => resolve(true),
        onCancel: () => resolve(false),
      });
    });
  }, [isDirty, isEditingForm, showConfirmation, submitting, t]);

  useImperativeHandle(ref, () => ({ confirmDiscardIfDirty }), [confirmDiscardIfDirty]);

  const backToPicker = state.mode === 'create' && state.fromPicker ? onBackToPicker : undefined;
  const handleCancelClick = useCallback(() => {
    void confirmDiscardIfDirty().then((ok) => {
      if (!ok) return;
      if (backToPicker) backToPicker();
      else onClose();
    });
  }, [backToPicker, confirmDiscardIfDirty, onClose]);

  const modeLabel =
    isNativeFamily(state.brand) && isEditingForm
      ? state.section
        ? // The title becomes the connection name, so the eyebrow keeps the provider visible.
          `${t(`providersPage.providerNames.${state.brand}`)} · ${t('providersPage.groupActions.edit')}`
        : t('providersPage.groupActions.create')
      : state.mode === 'create'
        ? t('providersPage.form.createEyebrow')
        : state.mode === 'edit'
          ? t('providersPage.form.editEyebrow')
          : t('providersPage.detail.title');
  const titleText =
    state.mode === 'pick' ? (
      t('providersPage.actions.addProvider')
    ) : (
      <span className={styles.sheetTitle}>
        <BrandLogo brand={state.brand} size={26} />
        <span className={styles.sheetTitleText}>
          <span className={styles.sheetTitleMode}>{modeLabel}</span>
          <span>{state.section?.name ?? t(`providersPage.providerNames.${state.brand}`)}</span>
        </span>
      </span>
    );

  const handleCreate = useCallback(
    async (input: ProviderEntryFormInput) => {
      if (mutationDisabled) return;
      setSubmitting(true);
      try {
        await workbench.createProvider(state.brand, input);
        onCreated();
      } finally {
        setSubmitting(false);
      }
    },
    [mutationDisabled, onCreated, state.brand, workbench]
  );

  const handleUpdate = useCallback(
    async (input: ProviderEntryFormInput) => {
      if (!state.resource || mutationDisabled || !isDirty) return;
      setSubmitting(true);
      try {
        await workbench.updateProvider(state.resource, input);
        onUpdated();
      } finally {
        setSubmitting(false);
      }
    },
    [isDirty, mutationDisabled, onUpdated, state.resource, workbench]
  );

  const handleNativeSubmit = async (draft: NativeConnectionFormOutput) => {
    if (submitDisabled || !isNativeFamily(state.brand)) return;
    setSubmitting(true);
    try {
      await workbench.saveNativeConnection(state.brand, state.section, draft);
      if (state.section) onUpdated();
      else onCreated();
    } finally {
      setSubmitting(false);
    }
  };

  const viewKey = `${state.brand}:${state.section?.source.groupIndex ?? state.resource?.id ?? 'new'}:${state.mode}`;

  const renderBody = () => {
    if (state.mode === 'pick') {
      return <ProviderPicker groups={pickerGroups} onPick={(brand) => onPick?.(brand)} />;
    }
    if (state.mode === 'detail') {
      if (!state.resource) {
        return null;
      }
      return <ResourceDetailView resource={state.resource} usageByProvider={usageByProvider} />;
    }
    if (isNativeFamily(state.brand)) {
      return (
        <BaseProviderForm
          key={viewKey}
          brand={state.brand}
          resource={null}
          nativeSection={state.section}
          mode={state.mode}
          mutating={formMutating}
          formId={formId}
          onNativeSubmit={handleNativeSubmit}
          onDirtyChange={handleDirtyChange}
        />
      );
    }
    if (isMultiProtocolSponsorBrand(state.brand)) {
      return (
        <SponsorProviderForm
          ambiguousProtocols={ambiguousProtocols}
          key={viewKey}
          brand={state.brand}
          resource={state.resource}
          mode={state.mode}
          mutating={formMutating}
          formId={formId}
          onSubmit={state.mode === 'create' ? handleCreate : handleUpdate}
          onDirtyChange={handleDirtyChange}
        />
      );
    }
    return (
      <BaseProviderForm
        key={viewKey}
        brand={state.brand}
        resource={state.resource}
        mode={state.mode}
        mutating={formMutating}
        formId={formId}
        onSubmit={state.mode === 'create' ? handleCreate : handleUpdate}
        onDirtyChange={handleDirtyChange}
      />
    );
  };

  const footer =
    state.mode === 'pick' ? (
      <button
        type="button"
        className={`${formStyles.footerBtn} ${formStyles.footerBtnGhost}`}
        onClick={onClose}
      >
        {t('common.close')}
      </button>
    ) : state.mode === 'detail' ? (
      <>
        <button
          type="button"
          className={`${formStyles.footerBtn} ${formStyles.footerBtnGhost}`}
          onClick={onClose}
        >
          {t('common.close')}
        </button>
        {state.resource ? (
          <button
            type="button"
            className={`${formStyles.footerBtn} ${formStyles.footerBtnPrimary}`}
            onClick={onSwitchToEdit}
            disabled={formMutating}
          >
            <IconPencil size={14} />
            {t('providersPage.actions.edit')}
          </button>
        ) : null}
      </>
    ) : (
      <>
        {state.section && (
          <button
            type="button"
            className={`${formStyles.footerBtn} ${styles.deleteConnection}`}
            disabled={formMutating}
            onClick={() => state.section && onDeleteConnection(state.section)}
          >
            {t('providersPage.groupActions.delete')}
          </button>
        )}
        <button
          type="button"
          className={`${formStyles.footerBtn} ${formStyles.footerBtnGhost}`}
          onClick={handleCancelClick}
          disabled={submitting}
        >
          {backToPicker ? t('common.back') : t('providersPage.actions.cancel')}
        </button>
        <button
          type="submit"
          form={formId}
          className={`${formStyles.footerBtn} ${formStyles.footerBtnPrimary}`}
          disabled={submitDisabled}
          aria-busy={submitting || undefined}
        >
          {submitting ? <IconLoader2 size={14} className={formStyles.spin} /> : null}
          {state.mode === 'create' && !isNativeFamily(state.brand)
            ? t('providersPage.actions.create')
            : t('providersPage.actions.save')}
        </button>
      </>
    );

  return (
    <Sheet
      open={state.open}
      viewKey={viewKey}
      onClose={onClose}
      size={descriptor.sheetSize}
      // The title carries mode and provider together; no eyebrow or route subtitle repeats them.
      title={titleText}
      footer={footer}
      closeDisabled={submitting}
      confirmClose={confirmDiscardIfDirty}
    >
      {renderBody()}
    </Sheet>
  );
}
