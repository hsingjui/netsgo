import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, scopedClientApi } from '@/lib/api';
import { scopedQueryKey, type ResourceScope } from '@/lib/resource-scope';
import type { VersionCheckResult, VersionInstallMethod, VersionTargetKind } from '@/types';

export interface VersionCheckTarget {
  kind: VersionTargetKind;
  id?: string;
  version?: string;
  installMethod?: VersionInstallMethod;
  os?: string;
  arch?: string;
  enabled?: boolean;
}

function normalizeMethod(method?: string): VersionInstallMethod {
  return method === 'service' || method === 'docker' || method === 'binary' ? method : 'binary';
}

export function versionCheckQueryKey(scope: ResourceScope, target: VersionCheckTarget) {
  return scopedQueryKey(
    scope,
    'version-check',
    target.kind,
    target.id || target.kind,
    target.version || '',
    normalizeMethod(target.installMethod),
    target.os || '',
    target.arch || '',
  );
}

function endpoint(scope: ResourceScope, target: VersionCheckTarget, force: boolean) {
  if (target.kind === 'client') {
    return scopedClientApi.versionCheck(scope, target.id || '', force);
  }
  return api.get<VersionCheckResult>(`/api/version/check${force ? '?force=true' : ''}`);
}

export function useVersionCheck(scope: ResourceScope, target: VersionCheckTarget) {
  const enabled = Boolean(target.enabled ?? true) && Boolean(target.version) && (target.kind === 'server' || Boolean(target.id));

  return useQuery({
    queryKey: versionCheckQueryKey(scope, target),
    queryFn: () => endpoint(scope, target, false) as Promise<VersionCheckResult>,
    enabled,
    retry: false,
    staleTime: 10 * 60 * 1000,
  });
}

export function useForceVersionCheck(scope: ResourceScope, target: VersionCheckTarget) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => endpoint(scope, target, true) as Promise<VersionCheckResult>,
    onSuccess: (result) => {
      queryClient.setQueryData(versionCheckQueryKey(scope, target), result);
    },
  });
}
