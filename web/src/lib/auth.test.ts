// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';

import { ApiError, authApi } from './api';
import { resolveCurrentPrincipal } from './auth';
import { useAuthStore } from '@/stores/auth-store';

describe('resolveCurrentPrincipal', () => {
  beforeEach(() => {
    localStorage.clear();
    useAuthStore.setState({ user: null, isAuthenticated: false, isResolved: false });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  test('retries after a transient /api/auth/me failure', async () => {
    const principal = { id: 'admin-1', username: 'admin', is_admin: true };
    const me = vi.spyOn(authApi, 'me')
      .mockRejectedValueOnce(new Error('temporary network failure'))
      .mockResolvedValueOnce(principal);

    await expect(resolveCurrentPrincipal()).resolves.toBeNull();
    expect(useAuthStore.getState()).toMatchObject({
      user: null,
      isAuthenticated: false,
      isResolved: false,
    });

    await expect(resolveCurrentPrincipal()).resolves.toEqual(principal);
    expect(me).toHaveBeenCalledTimes(2);
    expect(useAuthStore.getState()).toMatchObject({
      user: principal,
      isAuthenticated: true,
      isResolved: true,
    });
  });

  test('treats an authoritative 401 as resolved unauthenticated', async () => {
    vi.spyOn(authApi, 'me').mockRejectedValueOnce(new ApiError(401, 'Unauthorized'));

    await expect(resolveCurrentPrincipal()).resolves.toBeNull();
    expect(useAuthStore.getState()).toMatchObject({
      user: null,
      isAuthenticated: false,
      isResolved: true,
    });
  });
});
