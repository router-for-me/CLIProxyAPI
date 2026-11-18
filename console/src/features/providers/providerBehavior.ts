import type { ProviderBehaviorOptions, ProviderKeyConfig } from '@/types/provider';
import { getProviderBehaviorCapabilities } from './descriptors';
import type { ProviderBrand } from './types';

/** Keep protocol-specific form values out of other provider payloads. */
export function pickProviderBehavior(
  input: ProviderBehaviorOptions | ProviderKeyConfig,
  brand: ProviderBrand
): ProviderBehaviorOptions {
  const capabilities = getProviderBehaviorCapabilities(brand);
  return {
    ...(capabilities.alphaSearch ? { alphaSearch: input.alphaSearch } : {}),
    ...(capabilities.disableCodexCloaking
      ? { disableCodexCloaking: input.disableCodexCloaking }
      : {}),
    ...(capabilities.rebuildMidSystemMessage
      ? { rebuildMidSystemMessage: input.rebuildMidSystemMessage }
      : {}),
    ...(capabilities.supportPromptCacheKey
      ? { supportPromptCacheKey: input.supportPromptCacheKey }
      : {}),
  };
}
