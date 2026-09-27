import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { scopedConsoleSnapshotPath } from '@/lib/api';
import { parseResourceBootstrapSnapshot } from '@/lib/resource-bootstrap';
import { scopedQueryKey, type ResourceScope } from '@/lib/resource-scope';
import type { ServerStatus } from '@/types';

interface UseServerStatusOptions {
  enabled?: boolean;
  refetchOnMount?: boolean | 'always';
  staleTime?: number;
}

/**
 * Detailed host/process telemetry is administrator-global. Resource dialogs
 * use useResourceBootstrap below and never call this endpoint.
 */
export function useServerStatus(options: UseServerStatusOptions = {}) {
  return useQuery({
    queryKey: ['server-status'],
    queryFn: () => api.get<ServerStatus>('/api/status'),
    enabled: options.enabled ?? true,
    refetchOnMount: options.refetchOnMount,
    staleTime: options.staleTime ?? Infinity,
  });
}

/**
 * Load only the non-sensitive metadata needed to create resources in an
 * explicit user scope. The SSE snapshot writes through to the same cache key.
 */
export function useResourceBootstrap(scope: ResourceScope, options: UseServerStatusOptions = {}) {
  return useQuery({
    queryKey: scopedQueryKey(scope, 'resource-bootstrap'),
    queryFn: async () => {
      const snapshot = await api.get<unknown>(scopedConsoleSnapshotPath(scope));
      return parseResourceBootstrapSnapshot(snapshot);
    },
    enabled: options.enabled ?? true,
    refetchOnMount: options.refetchOnMount,
    staleTime: options.staleTime ?? Infinity,
  });
}
