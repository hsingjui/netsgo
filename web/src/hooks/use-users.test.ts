import { describe, expect, test, vi } from 'vitest';

import { fetchAllUsers, USER_LIST_PAGE_SIZE } from './use-users';
import type { ManagedUser } from '@/types';

function user(id: string): ManagedUser {
  return {
    id,
    username: id,
    is_admin: false,
    status: 'active',
    created_at: '2026-08-18T00:00:00Z',
    updated_at: '2026-08-18T00:00:00Z',
    operational: true,
  };
}

describe('fetchAllUsers', () => {
  test('follows every cursor so filters are not limited to the first page', async () => {
    const listUsers = vi.fn()
      .mockResolvedValueOnce({ items: [user('user-1')], next_cursor: 'next', has_more: true })
      .mockResolvedValueOnce({ items: [user('user-2')], has_more: false });

    await expect(fetchAllUsers(listUsers)).resolves.toEqual([user('user-1'), user('user-2')]);
    expect(listUsers).toHaveBeenNthCalledWith(1, { limit: USER_LIST_PAGE_SIZE, cursor: undefined });
    expect(listUsers).toHaveBeenNthCalledWith(2, { limit: USER_LIST_PAGE_SIZE, cursor: 'next' });
  });

  test('fails when a paginated response cannot advance', async () => {
    const listUsers = vi.fn().mockResolvedValue({ items: [], has_more: true });
    await expect(fetchAllUsers(listUsers)).rejects.toThrow('pagination did not advance');
  });
});
