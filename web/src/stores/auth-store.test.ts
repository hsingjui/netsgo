// @vitest-environment happy-dom
import { beforeEach, describe, expect, test } from 'vitest';

import { AUTH_STORAGE_KEY, getStoredAuthState, useAuthStore } from './auth-store';

describe('useAuthStore', () => {
  beforeEach(() => {
    useAuthStore.setState({ user: null, isAuthenticated: false });
    localStorage.clear();
  });

  test('initial state is unauthenticated', () => {
    const state = useAuthStore.getState();
    expect(state.user).toBeNull();
    expect(state.isAuthenticated).toBe(false);
  });

  test('setAuth stores user and marks authenticated', () => {
    useAuthStore.getState().setAuth({ username: 'admin' });

    const state = useAuthStore.getState();
    expect(state.user).toEqual({ username: 'admin' });
    expect(state.isAuthenticated).toBe(true);
  });

  test('logout clears user and authentication', () => {
    useAuthStore.getState().setAuth({ username: 'admin' });
    useAuthStore.getState().logout();

    const state = useAuthStore.getState();
    expect(state.user).toBeNull();
    expect(state.isAuthenticated).toBe(false);
  });

  test('persists auth state to localStorage', () => {
    useAuthStore.getState().setAuth({ username: 'admin' });

    const raw = localStorage.getItem(AUTH_STORAGE_KEY);
    expect(raw).not.toBeNull();
    const parsed = JSON.parse(raw!);
    expect(parsed.state.isAuthenticated).toBe(true);
    expect(parsed.state.user).toEqual({ username: 'admin' });
  });
});

describe('getStoredAuthState', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  test('returns unauthenticated when storage is empty', () => {
    expect(getStoredAuthState()).toEqual({ isAuthenticated: false, user: null });
  });

  test('reads persisted auth state', () => {
    localStorage.setItem(AUTH_STORAGE_KEY, JSON.stringify({
      state: { isAuthenticated: true, user: { username: 'admin' } },
      version: 0,
    }));

    expect(getStoredAuthState()).toEqual({
      isAuthenticated: true,
      user: { username: 'admin' },
    });
  });

  test('handles corrupt JSON gracefully', () => {
    localStorage.setItem(AUTH_STORAGE_KEY, 'not-json{{{');

    expect(getStoredAuthState()).toEqual({ isAuthenticated: false, user: null });
  });

  test('handles missing state fields', () => {
    localStorage.setItem(AUTH_STORAGE_KEY, JSON.stringify({ version: 0 }));

    expect(getStoredAuthState()).toEqual({ isAuthenticated: false, user: null });
  });
});
