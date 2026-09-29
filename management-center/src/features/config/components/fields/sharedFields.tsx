// 8 个高频字段的唯一渲染源：SectionCommon（常用 tab）与各正典分区共用这些组件，
// 两处渲染结构性不可能漂移（旧简单模式靠共享 JSX 常量达成同一目的）。
// 注意：只挂载激活 tab，所以 FieldAnchor 的 DOM id 不会重复。

import { useTranslation } from 'react-i18next';
import type { ReactNode } from 'react';
import { Input } from '@/components/ui/Input';
import type { VisualConfigValues } from '@/types/visualConfig';
import { SPONSORS } from '../../sponsors';
import { ApiKeysCardEditor } from '../blocks/ApiKeysCardEditor';
import { FieldAnchor, FieldGroup, ToggleRow } from './FieldPrimitives';
import fieldStyles from './Field.module.scss';

export type SharedFieldProps = {
  values: VisualConfigValues;
  disabled: boolean;
  onChange: (patch: Partial<VisualConfigValues>) => void;
  /** 可选的标签上方占位行（见 SponsorHintSpacer），仅在需要与代理 URL 字段对齐时传入。 */
  topExtra?: ReactNode;
};

export function HostField({ values, disabled, onChange, topExtra }: SharedFieldProps) {
  const { t } = useTranslation();
  return (
    <FieldAnchor fieldId="host">
      <Input
        label={t('config_management.visual.sections.server.host')}
        placeholder="0.0.0.0"
        value={values.host}
        onChange={(e) => onChange({ host: e.target.value })}
        disabled={disabled}
        topExtra={topExtra}
      />
    </FieldAnchor>
  );
}

export function PortField({
  values,
  disabled,
  onChange,
  error,
  topExtra,
}: SharedFieldProps & { error?: string }) {
  const { t } = useTranslation();
  return (
    <FieldAnchor fieldId="port">
      <Input
        label={t('config_management.visual.sections.server.port')}
        type="number"
        placeholder="8317"
        value={values.port}
        onChange={(e) => onChange({ port: e.target.value })}
        disabled={disabled}
        error={error}
        topExtra={topExtra}
      />
    </FieldAnchor>
  );
}

export function ProxyUrlField({ values, disabled, onChange }: SharedFieldProps) {
  const { t } = useTranslation();
  // 代理 URL 较长，字段跨两列；标签下方挂赞助跳转行（数据见 sponsors.ts，空则不渲染）。
  const sponsor = SPONSORS[0];
  return (
    <FieldAnchor fieldId="proxyUrl" wide>
      <Input
        label={t('config_management.visual.sections.network.proxy_url')}
        labelExtra={
          sponsor ? (
            <p className={fieldStyles.fieldSponsorHint}>
              {t('config_management.visual.sections.network.proxy_url_sponsor_hint')}{' '}
              <a
                className={fieldStyles.fieldSponsorLink}
                href={sponsor.url}
                target="_blank"
                rel="noopener noreferrer sponsored"
              >
                {sponsor.logo ? (
                  <img className={fieldStyles.fieldSponsorLogo} src={sponsor.logo} alt="" />
                ) : null}
                {sponsor.name}
              </a>
            </p>
          ) : undefined
        }
        placeholder="socks5://user:pass@127.0.0.1:1080/"
        value={values.proxyUrl}
        onChange={(e) => onChange({ proxyUrl: e.target.value })}
        disabled={disabled}
      />
    </FieldAnchor>
  );
}

/**
 * 与代理 URL 字段同排时的隐形占位行：渲染在标签上方（Input 的 topExtra），
 * 赞助商存在时把同排字段整体下移与赞助行同高，让输入框水平对齐，
 * 同时标签与输入框之间保持正常间距；无赞助商则不渲染。
 */
export function SponsorHintSpacer() {
  if (SPONSORS.length === 0) return null;
  return (
    <p className={fieldStyles.fieldSponsorSpacer} aria-hidden="true">
      &nbsp;
    </p>
  );
}

export function ApiKeysField({ values, disabled, onChange }: SharedFieldProps) {
  return (
    <FieldAnchor fieldId="apiKeys">
      <FieldGroup>
        <ApiKeysCardEditor
          value={values.apiKeysText}
          disabled={disabled}
          onChange={(apiKeysText) => onChange({ apiKeysText })}
        />
      </FieldGroup>
    </FieldAnchor>
  );
}

export function DebugToggle({ values, disabled, onChange }: SharedFieldProps) {
  const { t } = useTranslation();
  return (
    <FieldAnchor fieldId="debug">
      <ToggleRow
        title={t('config_management.visual.sections.system.debug')}
        description={t('config_management.visual.sections.system.debug_desc')}
        checked={values.debug}
        disabled={disabled}
        onChange={(debug) => onChange({ debug })}
      />
    </FieldAnchor>
  );
}

export function LoggingToFileToggle({ values, disabled, onChange }: SharedFieldProps) {
  const { t } = useTranslation();
  return (
    <FieldAnchor fieldId="loggingToFile">
      <ToggleRow
        title={t('config_management.visual.sections.system.logging_to_file')}
        description={t('config_management.visual.sections.system.logging_to_file_desc')}
        checked={values.loggingToFile}
        disabled={disabled}
        onChange={(loggingToFile) => onChange({ loggingToFile })}
      />
    </FieldAnchor>
  );
}

export function QuotaSwitchProjectToggle({ values, disabled, onChange }: SharedFieldProps) {
  const { t } = useTranslation();
  return (
    <FieldAnchor fieldId="quotaSwitchProject">
      <ToggleRow
        title={t('config_management.visual.sections.quota.switch_project')}
        description={t('config_management.visual.sections.quota.switch_project_desc')}
        checked={values.quotaSwitchProject}
        disabled={disabled}
        onChange={(quotaSwitchProject) => onChange({ quotaSwitchProject })}
      />
    </FieldAnchor>
  );
}

export function QuotaSwitchPreviewModelToggle({ values, disabled, onChange }: SharedFieldProps) {
  const { t } = useTranslation();
  return (
    <FieldAnchor fieldId="quotaSwitchPreviewModel">
      <ToggleRow
        title={t('config_management.visual.sections.quota.switch_preview_model')}
        description={t('config_management.visual.sections.quota.switch_preview_model_desc')}
        checked={values.quotaSwitchPreviewModel}
        disabled={disabled}
        onChange={(quotaSwitchPreviewModel) => onChange({ quotaSwitchPreviewModel })}
      />
    </FieldAnchor>
  );
}
