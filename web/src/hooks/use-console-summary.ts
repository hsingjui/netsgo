import { useQuery } from '@tanstack/react-query';

import { api } from '@/lib/api';
import { EMPTY_CONSOLE_SUMMARY } from '@/lib/console-summary';
import { scopedConsoleSnapshotPath } from '@/lib/api';
import { scopedQueryKey, type ResourceScope } from '@/lib/resource-scope';
import type { ConsoleSnapshot, ConsoleSummary } from '@/types';

export function useConsoleSummary(scope: ResourceScope | null) {
  return useQuery({
    queryKey: scope ? scopedQueryKey(scope, 'console-summary') : ['users', 'none', 'console-summary'],
    enabled: Boolean(scope),
    queryFn: async (): Promise<ConsoleSummary> => {
      if (!scope) return EMPTY_CONSOLE_SUMMARY;
      const snapshot = await api.get<ConsoleSnapshot>(scopedConsoleSnapshotPath(scope));
      return snapshot.summary ?? EMPTY_CONSOLE_SUMMARY;
    },
    staleTime: Infinity,
  });
}
