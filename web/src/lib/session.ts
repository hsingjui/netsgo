import { queryClient } from '@/lib/query-client';
import { useAuthStore } from '@/stores/auth-store';

/**
 * One client-side invalidation point for logout, session revocation, disabled
 * users, and an administrator-role change.  Resource data is never retained
 * after the identity that selected it is no longer valid.
 */
export function clearClientSession() {
  useAuthStore.getState().logout();
  queryClient.clear();
}

export function redirectToLogin() {
  if (typeof window !== 'undefined' && !window.location.hash.startsWith('#/login')) {
    window.location.hash = '#/login';
  }
}

export function clearClientSessionAndRedirect() {
  clearClientSession();
  redirectToLogin();
}
