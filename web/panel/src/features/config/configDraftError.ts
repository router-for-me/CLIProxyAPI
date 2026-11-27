import type { TFunction } from 'i18next';
import { ConfigDraftConflictError } from '@/services/api/configPatch';
import { InvalidOAuthRuleError } from './visualOAuthErrorRules';

export function configDraftErrorMessage(t: TFunction, error: unknown) {
  if (error instanceof InvalidOAuthRuleError)
    return t('config_management.visual.validation.invalid_oauth_error_rules', {
      channel: error.channel,
      number: error.ruleNumber,
    });
  if (error instanceof ConfigDraftConflictError)
    return t(`config_management.${error.reason}`, { path: error.path.join('.') });
  return error instanceof Error ? error.message : t('config_management.visual_mode_save_blocked');
}
