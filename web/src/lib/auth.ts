import { redirect } from '@tanstack/react-router';

import { ApiError, api, authApi } from '@/lib/api';
import { clearClientSession, clearClientSessionAndRedirect } from '@/lib/session';
import { useAuthStore } from '@/stores/auth-store';
import type { Principal } from '@/types';

let resolvePromise: Promise<Principal | null> | null = null;

/**
 * The cookie-backed session is the authority.  Persisted Zustand data is only
 * a short-lived rendering hint and is never used as the authorization source.
 */
export async function resolveCurrentPrincipal(force = false): Promise<Principal | null> {
  const state = useAuthStore.getState();
  if (!force && state.isResolved) {
    return state.isAuthenticated ? state.user : null;
  }
  if (resolvePromise) return resolvePromise;

  state.setResolving();
  resolvePromise = authApi.me()
    .then((principal) => {
      useAuthStore.getState().setAuth(principal);
      return principal;
    })
    .catch((error: unknown) => {
      if (error instanceof ApiError && error.status === 401) {
        useAuthStore.getState().setUnauthenticated();
        return null;
      }
      // A transient failure clears the persisted rendering hint but remains
      // unresolved so the next navigation retries /api/auth/me.
      useAuthStore.getState().setResolutionFailed();
      return null;
    })
    .finally(() => {
      resolvePromise = null;
    });
  return resolvePromise;
}

export function getCurrentPrincipal() {
  return useAuthStore.getState().user;
}

export async function requireConsoleAuth() {
  const user = await resolveCurrentPrincipal();
  if (!user) {
    throw redirect({ to: '/login' });
  }
  return { user };
}

export async function requireAdmin() {
  const { user } = await requireConsoleAuth();
  if (!user.is_admin) {
    throw redirect({ to: '/dashboard' });
  }
  return { user };
}

export async function redirectFromIndex() {
  const user = await resolveCurrentPrincipal();
  throw redirect({ to: user ? '/dashboard' : '/login' });
}

export async function requireLoginPage() {
  const user = await resolveCurrentPrincipal();
  if (user) {
    throw redirect({ to: '/dashboard' });
  }
}

export async function logoutCurrentSession() {
  try {
    await api.post('/api/auth/logout');
  } catch {
    // Clearing browser state is still required when the server session already
    // disappeared or the network request failed.
  }
  clearClientSession();
}

export function handleSessionInvalidation() {
  clearClientSessionAndRedirect();
}
