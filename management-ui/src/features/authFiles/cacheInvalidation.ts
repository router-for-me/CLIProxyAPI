import { useQuotaStore } from '@/stores/useQuotaStore';

type ModelsInvalidator = (names?: string[]) => void;

/** Invalidate every cache whose contents depend on an auth file's credentials. */
export const invalidateAuthFileDerivedCaches = (
  invalidateModels: ModelsInvalidator,
  names?: string[]
): void => {
  invalidateModels(names);
  useQuotaStore.getState().clearQuotaCache(names);
};
