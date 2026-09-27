import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { scopedClientApi } from '@/lib/api';
import {
  invalidateResourceScope,
  resourceScopeKey,
  scopedQueryKey,
  type ResourceScope,
} from '@/lib/resource-scope';
import type { Client } from '@/types';

export function buildClientsQueryKey(scope: ResourceScope | null) {
  return scope
    ? scopedQueryKey(scope, 'clients')
    : ['users', 'none', 'clients'] as const;
}

export function useClients(scope: ResourceScope | null, options: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: buildClientsQueryKey(scope),
    enabled: Boolean(scope) && (options.enabled ?? true),
    queryFn: () => {
      if (!scope) throw new Error('resource scope is required to load clients');
      return scopedClientApi.list(scope);
    },
    staleTime: Infinity,
  });
}

export function useDeleteClient(scope: ResourceScope) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (clientId: string) => scopedClientApi.delete(scope, clientId),
    onSuccess: () => {
      void invalidateResourceScope(queryClient, scope);
    },
  });
}

export function clientScopeCachePrefix(scope: ResourceScope) {
  return ['users', resourceScopeKey(scope)] as const;
}

export type ScopedClient = Client;
