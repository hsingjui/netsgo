import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

import { type UserListQuery, usersApi } from '@/lib/api';
import { adminUserResourceScope, removeResourceScope } from '@/lib/resource-scope';
import type { ManagedUser, UserListResponse } from '@/types';

export const USER_LIST_PAGE_SIZE = 50;

export function buildUsersQueryKey(query: UserListQuery = {}) {
  return [
    'admin-users',
    query.limit ?? USER_LIST_PAGE_SIZE,
    query.cursor ?? null,
    query.query ?? '',
    query.status ?? null,
    query.isAdmin ?? null,
  ] as const;
}

export function useUsers(query: UserListQuery = {}, options: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: buildUsersQueryKey(query),
    enabled: options.enabled ?? true,
    queryFn: () => usersApi.list({ limit: USER_LIST_PAGE_SIZE, ...query }),
    staleTime: 15_000,
  });
}

export async function fetchAllUsers(
  listUsers: (query: UserListQuery) => Promise<UserListResponse> = usersApi.list,
) {
  const items: ManagedUser[] = [];
  let cursor: string | undefined;
  let hasMore = true;
  while (hasMore) {
    const page = await listUsers({ limit: USER_LIST_PAGE_SIZE, cursor });
    items.push(...page.items);
    hasMore = page.has_more;
    if (!hasMore) break;
    if (!page.next_cursor || page.next_cursor === cursor) {
      throw new Error('user list pagination did not advance');
    }
    cursor = page.next_cursor;
  }
  return items;
}

export function useAllUsers(options: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: ['admin-users', 'all'] as const,
    enabled: options.enabled ?? true,
    queryFn: () => fetchAllUsers(),
    staleTime: 15_000,
  });
}

export function useManagedUser(userId: string | undefined) {
  return useQuery({
    queryKey: ['admin-user', userId] as const,
    enabled: Boolean(userId),
    queryFn: () => {
      if (!userId) throw new Error('user ID is required');
      return usersApi.get(userId);
    },
  });
}

export function useManagedUserDeletionImpact(userId: string | undefined) {
  return useQuery({
    queryKey: ['admin-user', userId, 'deletion-impact'] as const,
    enabled: Boolean(userId),
    queryFn: () => {
      if (!userId) throw new Error('user ID is required');
      return usersApi.deletionImpact(userId);
    },
    staleTime: 0,
    refetchOnMount: 'always',
  });
}

function useUserMutation<TInput, TResult = ManagedUser>(
  mutationFn: (input: TInput) => Promise<TResult>,
  options: { removeTargetScope?: (input: TInput) => string | undefined } = {},
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn,
    onSuccess: (_result, input) => {
      void queryClient.invalidateQueries({ queryKey: ['admin-users'] });
      void queryClient.invalidateQueries({ queryKey: ['admin-user'] });
      const targetUserId = options.removeTargetScope?.(input);
      if (targetUserId) removeResourceScope(queryClient, adminUserResourceScope(targetUserId));
    },
  });
}

export function useCreateUser() {
  return useUserMutation((input: { username: string; password: string }) => usersApi.create(input));
}

export function useUpdateManagedUsername() {
  return useUserMutation((input: { userId: string; username: string }) => usersApi.updateUsername(input.userId, input.username));
}

export function useUpdateManagedPassword() {
  return useUserMutation((input: { userId: string; password: string }) => usersApi.updatePassword(input.userId, input.password));
}

export function useSetManagedAdmin() {
  return useUserMutation(
    (input: { userId: string; isAdmin: boolean }) => usersApi.setAdmin(input.userId, input.isAdmin),
    { removeTargetScope: (input) => input.userId },
  );
}

export function useDisableManagedUser() {
  return useUserMutation(
    (userId: string) => usersApi.disable(userId),
    { removeTargetScope: (userId) => userId },
  );
}

export function useEnableManagedUser() {
  return useUserMutation((userId: string) => usersApi.enable(userId));
}

export function useDeleteManagedUser() {
  return useUserMutation(
    (userId: string) => usersApi.delete(userId),
    { removeTargetScope: (userId) => userId },
  );
}

export function useRevokeManagedUserSessions() {
  return useUserMutation((userId: string) => usersApi.revokeSessions(userId));
}
