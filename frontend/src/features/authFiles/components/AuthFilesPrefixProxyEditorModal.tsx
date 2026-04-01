import { useTranslation } from 'react-i18next';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { Input } from '@/components/ui/Input';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import type {
  PrefixProxyEditorField,
  PrefixProxyEditorFieldValue,
  PrefixProxyEditorState,
} from '@/features/authFiles/hooks/useAuthFilesPrefixProxyEditor';
import styles from '@/pages/AuthFilesPage.module.scss';

export type AuthFilesPrefixProxyEditorModalProps = {
  disableControls: boolean;
  editor: PrefixProxyEditorState | null;
  updatedText: string;
  dirty: boolean;
  onClose: () => void;
  onCopyText: (text: string) => void | Promise<void>;
  onSave: () => void;
  onChange: (field: PrefixProxyEditorField, value: PrefixProxyEditorFieldValue) => void;
};

export function AuthFilesPrefixProxyEditorModal(props: AuthFilesPrefixProxyEditorModalProps) {
  const { t } = useTranslation();
  const { disableControls, editor, updatedText, dirty, onClose, onCopyText, onSave, onChange } =
    props;
  const formatJsonText = (text: string) => {
    if (!text) return '';
    try {
      return JSON.stringify(JSON.parse(text), null, 2);
    } catch {
      return text;
    }
  };
  const previewText = formatJsonText(updatedText);

  return (
    <Modal
      open={Boolean(editor)}
      onClose={onClose}
      closeDisabled={editor?.saving === true}
      width={720}
      title={
        editor?.fileName
          ? t('auth_files.auth_field_editor_title', { name: editor.fileName })
          : t('auth_files.prefix_proxy_button')
      }
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={editor?.saving === true}>
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
            disabled={disableControls || editor?.saving === true || !dirty || !editor?.json}
          >
            {t('common.save')}
          </Button>
        </>
      }
    >
      {editor && (
        <div className={styles.prefixProxyEditor}>
          {editor.loading ? (
            <div className={styles.prefixProxyLoading}>
              <LoadingSpinner size={14} />
              <span>{t('auth_files.prefix_proxy_loading')}</span>
            </div>
          ) : (
            <>
              {editor.error && <div className={styles.prefixProxyError}>{editor.error}</div>}
              <div className={styles.prefixProxyJsonWrapper}>
                <label className={styles.prefixProxyLabel}>
                  {t('auth_files.prefix_proxy_info_label')}
                </label>
                <textarea
                  className={styles.prefixProxyTextarea}
                  rows={8}
                  readOnly
                  value={editor.fileInfoText}
                />
              </div>
              <div className={styles.prefixProxyJsonWrapper}>
                <label className={styles.prefixProxyLabel}>
                  {t('auth_files.prefix_proxy_source_label')}
                </label>
                <textarea
                  className={styles.prefixProxyTextarea}
                  rows={10}
                  readOnly
                  value={previewText}
                />
              </div>
              <div className={styles.prefixProxyFields}>
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
                <div className="form-group">
                  <label>{t('auth_files.excluded_models_label')}</label>
                  <textarea
                    className="input"
                    value={editor.excludedModelsText}
                    placeholder={t('auth_files.excluded_models_placeholder')}
                    rows={4}
                    disabled={disableControls || editor.saving || !editor.json}
                    onChange={(e) => onChange('excludedModelsText', e.target.value)}
                  />
                  <div className="hint">{t('auth_files.excluded_models_hint')}</div>
                </div>
                <Input
                  label={t('auth_files.disable_cooling_label')}
                  value={editor.disableCooling}
                  placeholder={t('auth_files.disable_cooling_placeholder')}
                  hint={t('auth_files.disable_cooling_hint')}
                  disabled={disableControls || editor.saving || !editor.json}
                  onChange={(e) => onChange('disableCooling', e.target.value)}
                />
                <Input
                  label={t('auth_files.note_label')}
                  value={editor.note}
                  placeholder={t('auth_files.note_placeholder')}
                  hint={t('auth_files.note_hint')}
                  disabled={disableControls || editor.saving || !editor.json}
                  onChange={(e) => onChange('note', e.target.value)}
                />
                {editor.isCodexFile && (
                  <div className="form-group">
                    <label>{t('ai_providers.codex_websockets_label')}</label>
                    <ToggleSwitch
                      checked={Boolean(editor.websockets)}
                      disabled={disableControls || editor.saving || !editor.json}
                      ariaLabel={t('ai_providers.codex_websockets_label')}
                      onChange={(value) => onChange('websockets', value)}
                    />
                    <div className="hint">{t('ai_providers.codex_websockets_hint')}</div>
                  </div>
                )}
              </div>
            </>
          )}
        </div>
      )}
    </Modal>
  );
}
