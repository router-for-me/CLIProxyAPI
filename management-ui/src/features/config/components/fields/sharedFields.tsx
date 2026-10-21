// 6 个高频字段的唯一渲染源：SectionCommon（常用 tab）与各正典分区共用这些组件，
// 两处渲染结构性不可能漂移（旧简单模式靠共享 JSX 常量达成同一目的）。
// 注意：只挂载激活 tab，所以设置行的 DOM id 不会重复。

import { useTranslation } from 'react-i18next';
import type { VisualConfigValues } from '@/types/visualConfig';
import { SPONSORS } from '../../sponsors';
import { ApiKeysCardEditor } from '../blocks/ApiKeysCardEditor';
import { SettingCell, SettingList, TextSetting, ToggleSetting } from './FieldPrimitives';
import fieldStyles from './Field.module.scss';

export type SharedFieldProps = {
  values: VisualConfigValues;
  disabled: boolean;
  onChange: (patch: Partial<VisualConfigValues>) => void;
  /**
   * 原样转给设置行。SettingList 只看直接子元素的 props 排布两列，
   * 所以「占整行」必须写在调用处（或由 SettingList 给落单行注入），不能藏在组件内部。
   */
  wide?: boolean;
};

export function HostField({ values, disabled, onChange, wide }: SharedFieldProps) {
  const { t } = useTranslation();
  return (
    <TextSetting
      fieldId="host"
      wide={wide}
      label={t('config_management.visual.sections.server.host')}
      placeholder="0.0.0.0"
      value={values.host}
      onChange={(host) => onChange({ host })}
      disabled={disabled}
    />
  );
}

export function PortField({
  values,
  disabled,
  onChange,
  error,
  wide,
}: SharedFieldProps & { error?: string }) {
  const { t } = useTranslation();
  return (
    <TextSetting
      fieldId="port"
      wide={wide}
      label={t('config_management.visual.sections.server.port')}
      type="number"
      size="sm"
      placeholder="8317"
      value={values.port}
      onChange={(port) => onChange({ port })}
      disabled={disabled}
      error={error}
    />
  );
}

export function ProxyUrlField({ values, disabled, onChange, wide }: SharedFieldProps) {
  const { t } = useTranslation();
  // 代理 URL 较长，调用处传 wide 独占整行；说明位挂赞助跳转（数据见 sponsors.ts，空则不渲染）。
  const sponsor = SPONSORS[0];
  return (
    <TextSetting
      fieldId="proxyUrl"
      wide={wide}
      size="lg"
      label={t('config_management.visual.sections.network.proxy_url')}
      description={
        sponsor ? (
          <>
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
          </>
        ) : undefined
      }
      placeholder="socks5://user:pass@127.0.0.1:1080/"
      value={values.proxyUrl}
      onChange={(proxyUrl) => onChange({ proxyUrl })}
      disabled={disabled}
    />
  );
}

export function ApiKeysField({ values, disabled, onChange }: SharedFieldProps) {
  return (
    <SettingList>
      <SettingCell fieldId="apiKeys">
        <ApiKeysCardEditor
          value={values.apiKeysText}
          disabled={disabled}
          onChange={(apiKeysText) => onChange({ apiKeysText })}
        />
      </SettingCell>
    </SettingList>
  );
}

export function DebugToggle({ values, disabled, onChange, wide }: SharedFieldProps) {
  const { t } = useTranslation();
  return (
    <ToggleSetting
      fieldId="debug"
      wide={wide}
      label={t('config_management.visual.sections.system.debug')}
      description={t('config_management.visual.sections.system.debug_desc')}
      checked={values.debug}
      disabled={disabled}
      onChange={(debug) => onChange({ debug })}
    />
  );
}

export function LoggingToFileToggle({ values, disabled, onChange, wide }: SharedFieldProps) {
  const { t } = useTranslation();
  return (
    <ToggleSetting
      fieldId="loggingToFile"
      wide={wide}
      label={t('config_management.visual.sections.system.logging_to_file')}
      description={t('config_management.visual.sections.system.logging_to_file_desc')}
      checked={values.loggingToFile}
      disabled={disabled}
      onChange={(loggingToFile) => onChange({ loggingToFile })}
    />
  );
}
