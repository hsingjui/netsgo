import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { scopedKeyApi } from '@/lib/api';
import { invalidateResourceScope, scopedQueryKey, type ResourceScope } from '@/lib/resource-scope';

export function useAdminKeys(scope: ResourceScope) {
  return useQuery({
    queryKey: scopedQueryKey(scope, 'keys'),
    queryFn: () => scopedKeyApi.list(scope),
  });
}

export function useCreateAPIKey(scope: ResourceScope) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: { name: string; permissions?: string[]; max_uses?: number; expires_in?: string }) =>
      scopedKeyApi.create(scope, data),
    onSuccess: () => {
      void invalidateResourceScope(queryClient, scope);
    },
  });
}

export function useEnableAPIKey(scope: ResourceScope) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => scopedKeyApi.enable(scope, id),
    onSuccess: () => {
      void invalidateResourceScope(queryClient, scope);
    },
  });
}

export function useDisableAPIKey(scope: ResourceScope) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => scopedKeyApi.disable(scope, id),
    onSuccess: () => {
      void invalidateResourceScope(queryClient, scope);
    },
  });
}

export function useDeleteAPIKey(scope: ResourceScope) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => scopedKeyApi.delete(scope, id),
    onSuccess: () => {
      void invalidateResourceScope(queryClient, scope);
    },
  });
}
