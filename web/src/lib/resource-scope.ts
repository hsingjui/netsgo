/**
 * A resource scope is always explicit at the UI/API boundary.  `self` never
 * carries a user ID, while an administrator operating on somebody else uses
 * the target user's ID in both the route and the API path.
 */
export type ResourceScope =
  | { kind: 'self' }
  | { kind: 'admin-user'; userId: string };

export const SELF_RESOURCE_SCOPE: ResourceScope = { kind: 'self' };

export function adminUserResourceScope(userId: string): ResourceScope {
  return { kind: 'admin-user', userId };
}

export function resourceScopeKey(scope: ResourceScope): string {
  return scope.kind === 'self' ? 'self' : scope.userId;
}

export function sameResourceScope(left: ResourceScope, right: ResourceScope): boolean {
  if (left.kind === 'self' || right.kind === 'self') {
    return left.kind === right.kind;
  }
  return left.userId === right.userId;
}

export function scopedQueryKey(scope: ResourceScope, ...parts: readonly unknown[]) {
  return ['users', resourceScopeKey(scope), ...parts] as const;
}

export function invalidateResourceScope(queryClient: QueryClient, scope: ResourceScope) {
  return queryClient.invalidateQueries({ queryKey: ['users', resourceScopeKey(scope)] });
}

export function removeResourceScope(queryClient: QueryClient, scope: ResourceScope) {
  queryClient.removeQueries({ queryKey: ['users', resourceScopeKey(scope)] });
}
import type { QueryClient } from '@tanstack/react-query';
