import { useCallback, useId, useMemo, type MouseEvent } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { Trans, useTranslation } from 'react-i18next';
import { Sheet } from '@/components/ui/Sheet';
import { Button } from '@/components/ui/Button';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { Input } from '@/components/ui/Input';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { useNotificationStore } from '@/stores';
import type {
  PrefixProxyEditorField,
  PrefixProxyEditorFieldValue,
  PrefixProxyEditorState,
} from '@/features/authFiles/hooks/useAuthFilesPrefixProxyEditor';
import {
  supportsAuthFileSiwc,
  supportsAuthFileUsingApi,
  supportsAuthFileWebsockets,
} from '@/features/authFiles/constants';
import { MAX_CREDENTIAL_WEIGHT } from '@/utils/credentialWeight';
import { AuthFileExcludedModelsField } from './AuthFileExcludedModelsField';
import { AuthFilePolicyFields } from './AuthFilePolicyFields';
import { FieldLabel } from '@/components/ui/FieldLabel';
import { credentialPolicyError, readCredentialPolicy } from '../credentialPolicy';
import styles from './AuthFileDetailsSheet.module.scss';

/** API 边界归一化补写的派生字段——INFO 视图里只展示后端原始形状，避免重复噪音。 */
const DERIVED_INFO_KEYS = [
  'successCount',
  'failureCount',
  'recentRequests',
  'runtimeOnly',
  'authIndex',
  'statusMessage',
  'modified',
  // 'email' 不在此列：后端原始键名与 camelCase 同形，删掉会藏起真实数据。
  'projectId',
];

export type AuthFileDetailsSheetProps = {
  disableControls: boolean;
  editor: PrefixProxyEditorState | null;
  updatedText: string;
  dirty: boolean;
  onClose: () => void;
  onCopyText: (text: string) => void | Promise<void>;
  onSave: () => void;
  onChange: (field: PrefixProxyEditorField, value: PrefixProxyEditorFieldValue) => void;
};

/**
 * 凭证详情/编辑抽屉：替代旧的居中 Modal，与提供商工作台的 Sheet 模式一致。
 * 脏状态下关闭（Escape/遮罩/×/取消）先走确认对话框。
 */
export function AuthFileDetailsSheet(props: AuthFileDetailsSheetProps) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const hintIdBase = useId();
  const { disableControls, editor, updatedText, dirty, onClose, onCopyText, onSave, onChange } =
    props;
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);

  const confirmClose = useCallback((): boolean | Promise<boolean> => {
    if (!dirty || editor?.saving === true) return true;
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
  }, [dirty, editor?.saving, showConfirmation, t]);

  const handleCancelClick = useCallback(() => {
    void Promise.resolve(confirmClose()).then((ok) => {
      if (ok) onClose();
    });
  }, [confirmClose, onClose]);

  const handleSettingsLinkClick = useCallback(
    (event: MouseEvent<HTMLAnchorElement>) => {
      event.preventDefault();
      if (editor?.saving) return;

      void Promise.resolve(confirmClose()).then((ok) => {
        if (ok) void navigate('/config?field=routingStrategy');
      });
    },
    [confirmClose, editor?.saving, navigate]
  );

  const formatJsonText = (text: string) => {
    if (!text) return '';
    try {
      return JSON.stringify(JSON.parse(text), null, 2);
    } catch {
      return text;
    }
  };
  const previewText = formatJsonText(updatedText);
  const invalidContentPreview = editor?.invalidContentPreview ?? '';
  const fileInfoText = editor?.fileInfoText ?? '';
  const displayInfoText = useMemo(() => {
    if (!fileInfoText) return '';
    try {
      const parsed = JSON.parse(fileInfoText) as unknown;
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
        const record = parsed as Record<string, unknown>;
        DERIVED_INFO_KEYS.forEach((key) => {
          delete record[key];
        });
        return JSON.stringify(record, null, 2);
      }
    } catch {
      /* 非 JSON 原样展示 */
    }
    return fileInfoText;
  }, [fileInfoText]);

  return (
    <Sheet
      open={Boolean(editor)}
      onClose={onClose}
      confirmClose={confirmClose}
      size="md"
      closeDisabled={editor?.saving === true}
      eyebrow={t('auth_files.prefix_proxy_button')}
      title={editor?.fileName ?? ''}
      footer={
        <>
          <Button
            variant="secondary"
            onClick={handleCancelClick}
            disabled={editor?.saving === true}
          >
            {dirty ? t('common.cancel') : t('common.close')}
          </Button>
          <Button
            variant="secondary"
            onClick={() => {
              if (!updatedText) return;
              void onCopyText(updatedText);
            }}
            disabled={editor?.saving === true || !updatedText}
          >
            {t('common.copy')}
          </Button>
          <Button
            onClick={onSave}
            loading={editor?.saving === true}
            disabled={
              disableControls ||
              editor?.saving === true ||
              !dirty ||
              !editor?.json ||
              Boolean(editor?.headersTouched && editor.headersError) ||
              Boolean(editor?.weightError) ||
              Boolean(credentialPolicyError(editor?.policy))
            }
          >
            {t('common.save')}
          </Button>
        </>
      }
    >
      {editor && (
        <div className={styles.editor}>
          {editor.loading ? (
            <div className={styles.loading}>
              <LoadingSpinner size={14} />
              <span>{t('auth_files.prefix_proxy_loading')}</span>
            </div>
          ) : (
            <>
              {editor.error && <div className={styles.error}>{editor.error}</div>}
              <div className={styles.jsonWrapper}>
                <label className={styles.label}>{t('auth_files.prefix_proxy_info_label')}</label>
                <textarea className={styles.textarea} rows={8} readOnly value={displayInfoText} />
              </div>
              <div className={styles.jsonWrapper}>
                <label className={styles.label}>
                  {editor.json
                    ? t('auth_files.prefix_proxy_source_label')
                    : t('auth_files.prefix_proxy_invalid_content_label')}
                </label>
                {editor.json ? (
                  <textarea className={styles.textarea} rows={10} readOnly value={previewText} />
                ) : (
                  <pre className={styles.invalidPreview}>{invalidContentPreview}</pre>
                )}
              </div>
              {editor.json && (
                <div className={styles.fields}>
                  <Input
                    label={t('auth_files.prefix_label')}
                    value={editor.prefix}
                    disabled={disableControls || editor.saving || !editor.json}
                    onChange={(e) => onChange('prefix', e.target.value)}
                  />
                  <Input
                    label={t('auth_files.proxy_url_label')}
                    value={editor.proxyUrl}
                    placeholder={t('auth_files.proxy_url_placeholder')}
                    disabled={disableControls || editor.saving || !editor.json}
                    onChange={(e) => onChange('proxyUrl', e.target.value)}
                  />
                  <Input
                    label={t('auth_files.priority_label')}
                    value={editor.priority}
                    placeholder={t('auth_files.priority_placeholder')}
                    hint={t('auth_files.priority_hint')}
                    disabled={disableControls || editor.saving || !editor.json}
                    onChange={(e) => onChange('priority', e.target.value)}
                  />
                  <Input
                    label={t('auth_files.weight_label')}
                    type="number"
                    step="1"
                    max={MAX_CREDENTIAL_WEIGHT}
                    value={editor.weight}
                    placeholder="1"
                    // 取值语义进标签后的 InfoTip；「需开启加权轮询才生效」不读就会白填，
                    // 且含跳转链接（提示里无法键盘到达），作为常驻 note 留在输入框下方。
                    hint={t('auth_files.weight_hint')}
                    note={
                      <Trans
                        i18nKey="auth_files.weight_strategy_note"
                        components={{
                          settingsLink: (
                            <Link
                              className={styles.settingsLink}
                              to="/config?field=routingStrategy"
                              onClick={handleSettingsLinkClick}
                            />
                          ),
                        }}
                      />
                    }
                    error={editor.weightError ?? undefined}
                    disabled={disableControls || editor.saving || !editor.json}
                    onChange={(e) => onChange('weight', e.target.value)}
                  />
                  <div className="form-group">
                    <FieldLabel
                      as="label"
                      label={t('auth_files.disable_cooling_label')}
                      hint={t('auth_files.disable_cooling_hint')}
                      hintId={`${hintIdBase}-disableCooling`}
                    />
                    <ToggleSwitch
                      checked={editor.disableCooling}
                      onChange={(value) => onChange('disableCooling', value)}
                      disabled={disableControls || editor.saving || !editor.json}
                      ariaLabel={t('auth_files.disable_cooling_label')}
                      ariaDescribedBy={`${hintIdBase}-disableCooling`}
                    />
                  </div>
                  {supportsAuthFileWebsockets(editor.providerKey) && (
                    <div className="form-group">
                      <FieldLabel
                        as="label"
                        label={t('auth_files.websockets_label')}
                        hint={t('auth_files.websockets_hint')}
                        hintId={`${hintIdBase}-websockets`}
                      />
                      <ToggleSwitch
                        checked={editor.websockets}
                        onChange={(value) => onChange('websockets', value)}
                        disabled={disableControls || editor.saving || !editor.json}
                        ariaLabel={t('auth_files.websockets_label')}
                        ariaDescribedBy={`${hintIdBase}-websockets`}
                      />
                    </div>
                  )}
                  {supportsAuthFileSiwc(editor.providerKey) && (
                    <div className="form-group">
                      <FieldLabel
                        as="label"
                        label={t('auth_files.use_siwc_label')}
                        hint={t('auth_files.use_siwc_hint')}
                        hintId={`${hintIdBase}-useSiwc`}
                      />
                      <ToggleSwitch
                        checked={editor.useSiwc}
                        onChange={(value) => onChange('useSiwc', value)}
                        disabled={disableControls || editor.saving || !editor.json}
                        ariaLabel={t('auth_files.use_siwc_label')}
                        ariaDescribedBy={`${hintIdBase}-useSiwc`}
                      />
                    </div>
                  )}
                  {supportsAuthFileUsingApi(editor.providerKey) && (
                    <div className="form-group">
                      <FieldLabel
                        as="label"
                        label={t('auth_files.using_api_label')}
                        hint={t('auth_files.using_api_hint')}
                        hintId={`${hintIdBase}-usingApi`}
                      />
                      <ToggleSwitch
                        checked={editor.usingApi}
                        onChange={(value) => onChange('usingApi', value)}
                        disabled={disableControls || editor.saving || !editor.json}
                        ariaLabel={t('auth_files.using_api_label')}
                        ariaDescribedBy={`${hintIdBase}-usingApi`}
                      />
                    </div>
                  )}
                  <AuthFileExcludedModelsField
                    fileName={editor.fileName}
                    value={editor.excludedModelsText}
                    disabled={disableControls || editor.saving || !editor.json}
                    onChange={(value) => onChange('excludedModelsText', value)}
                  />
                  <div className="form-group">
                    <label>{t('auth_files.headers_label')}</label>
                    <textarea
                      className={`input ${editor.headersError ? styles.textareaInvalid : ''}`}
                      value={editor.headersText}
                      placeholder={t('auth_files.headers_placeholder')}
                      rows={4}
                      aria-invalid={Boolean(editor.headersError)}
                      disabled={disableControls || editor.saving || !editor.json}
                      onChange={(e) => onChange('headersText', e.target.value)}
                    />
                    {editor.headersError && <div className="error-box">{editor.headersError}</div>}
                  </div>
                  <AuthFilePolicyFields
                    draft={editor.policy ?? readCredentialPolicy(editor.json)}
                    disabled={disableControls || editor.saving}
                    onChange={onChange}
                  />
                  <Input
                    label={t('auth_files.note_label')}
                    value={editor.note}
                    placeholder={t('auth_files.note_placeholder')}
                    disabled={disableControls || editor.saving || !editor.json}
                    onChange={(e) => onChange('note', e.target.value)}
                  />
                </div>
              )}
            </>
          )}
        </div>
      )}
    </Sheet>
  );
}
