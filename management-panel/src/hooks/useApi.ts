/**
 * 通用 API 调用 Hook
 */

import { useState, useCallback } from 'react';
import { useNotificationStore } from '@/stores';

interface UseApiOptions<T> {
  onSuccess?: (data: T) => void;
  onError?: (error: Error) => void;
  showSuccessNotification?: boolean;
  showErrorNotification?: boolean;
  successMessage?: string;
}

export function useApi<T = any, Args extends any[] = any[]>(
  apiFunction: (...args: Args) => Promise<T>,
  options: UseApiOptions<T> = {}
) {
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<Error | null>(null);

  const { showNotification } = useNotificationStore();

  const execute = useCallback(
    async (...args: Args) => {
      setLoading(true);
      setError(null);

      try {
        const result = await apiFunction(...args);
        setData(result);

        if (options.showSuccessNotification && options.successMessage) {
          showNotification(options.successMessage, 'success');
        }

        options.onSuccess?.(result);
        return result;
      } catch (err) {
        const errorObj = err as Error;
        setError(errorObj);

        if (options.showErrorNotification !== false) {
          showNotification(errorObj.message, 'error');
        }

        options.onError?.(errorObj);
        throw errorObj;
      } finally {
        setLoading(false);
      }
    },
    [apiFunction, options, showNotification]
  );

  const reset = useCallback(() => {
    setData(null);
    setError(null);
    setLoading(false);
  }, []);

  return { data, loading, error, execute, reset };
}
