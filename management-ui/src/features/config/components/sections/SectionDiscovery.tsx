import { useTranslation } from 'react-i18next';
import { Collapsible } from '@/components/ui/Collapsible';
import type { ConfigSectionProps } from '../../types';
import { BlockSetting, SettingList, TextSetting, ToggleSetting } from '../fields/FieldPrimitives';
import { StringListEditor } from '../blocks/StringListEditor';
import { getValidationMessage } from '../blocks/shared';

/** Kept mounted inside native details so search can reveal every discovery field. */
export function SectionDiscovery({
  values,
  validationErrors,
  disabled,
  onChange,
}: ConfigSectionProps) {
  const { t } = useTranslation();

  return (
    <Collapsible
      label={t('config_management.visual.serverExtras.discoveryTitle')}
      hint={t('config_management.visual.serverExtras.discoveryHint')}
      defaultOpen={false}
    >
      <SettingList>
        <ToggleSetting
          fieldId="discoveryEnabled"
          wide
          label={t('config_management.visual.serverExtras.discoveryEnabled.label')}
          description={t('config_management.visual.serverExtras.discoveryEnabled.hint')}
          checked={values.discoveryEnabled}
          disabled={disabled}
          onChange={(discoveryEnabled) => onChange({ discoveryEnabled })}
        />
        <TextSetting
          fieldId="discoveryServiceName"
          label={t('config_management.visual.serverExtras.discoveryServiceName.label')}
          description={t('config_management.visual.serverExtras.discoveryServiceName.hint')}
          value={values.discoveryServiceName}
          disabled={disabled}
          onChange={(discoveryServiceName) => onChange({ discoveryServiceName })}
        />
        <TextSetting
          fieldId="discoveryServiceType"
          label={t('config_management.visual.serverExtras.discoveryServiceType.label')}
          description={t('config_management.visual.serverExtras.discoveryServiceType.hint')}
          placeholder="_ai-gateway._tcp"
          error={getValidationMessage(t, validationErrors?.discoveryServiceType)}
          value={values.discoveryServiceType}
          disabled={disabled}
          onChange={(discoveryServiceType) => onChange({ discoveryServiceType })}
        />
        <BlockSetting
          fieldId="discoverySubtypes"
          label={t('config_management.visual.serverExtras.discoverySubtypes.label')}
          description={t('config_management.visual.serverExtras.discoverySubtypes.hint')}
        >
          <StringListEditor
            value={values.discoverySubtypes}
            disabled={disabled}
            placeholder="_responses"
            inputAriaLabel={t('config_management.visual.serverExtras.discoverySubtypes.label')}
            onChange={(discoverySubtypes) => onChange({ discoverySubtypes })}
          />
        </BlockSetting>
        <BlockSetting
          fieldId="discoveryInterfacesInclude"
          label={t('config_management.visual.serverExtras.discoveryInterfacesInclude.label')}
          description={t('config_management.visual.serverExtras.discoveryInterfacesInclude.hint')}
        >
          <StringListEditor
            value={values.discoveryInterfacesInclude}
            disabled={disabled}
            placeholder="en*"
            inputAriaLabel={t(
              'config_management.visual.serverExtras.discoveryInterfacesInclude.label'
            )}
            onChange={(discoveryInterfacesInclude) => onChange({ discoveryInterfacesInclude })}
          />
        </BlockSetting>
        <BlockSetting
          fieldId="discoveryInterfacesExclude"
          label={t('config_management.visual.serverExtras.discoveryInterfacesExclude.label')}
          description={t('config_management.visual.serverExtras.discoveryInterfacesExclude.hint')}
        >
          <StringListEditor
            value={values.discoveryInterfacesExclude}
            disabled={disabled}
            placeholder="en*"
            inputAriaLabel={t(
              'config_management.visual.serverExtras.discoveryInterfacesExclude.label'
            )}
            onChange={(discoveryInterfacesExclude) => onChange({ discoveryInterfacesExclude })}
          />
        </BlockSetting>
        <ToggleSetting
          fieldId="discoveryAuthRequired"
          label={t('config_management.visual.serverExtras.discoveryAuthRequired.label')}
          description={t('config_management.visual.serverExtras.discoveryAuthRequired.hint')}
          checked={values.discoveryAuthRequired}
          disabled={disabled}
          onChange={(discoveryAuthRequired) => onChange({ discoveryAuthRequired })}
        />
        <ToggleSetting
          fieldId="discoveryAdvertiseManagement"
          label={t('config_management.visual.serverExtras.discoveryAdvertiseManagement.label')}
          description={t('config_management.visual.serverExtras.discoveryAdvertiseManagement.hint')}
          checked={values.discoveryAdvertiseManagement}
          disabled={disabled}
          onChange={(discoveryAdvertiseManagement) => onChange({ discoveryAdvertiseManagement })}
        />
      </SettingList>
    </Collapsible>
  );
}
