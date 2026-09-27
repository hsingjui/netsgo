import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import type { Principal } from '@/types';

export const AUTH_STORAGE_KEY = 'netsgo-auth';

interface AuthState {
  user: Principal | null;
  isAuthenticated: boolean;
  /** True only after this browser session has asked the server for /api/auth/me. */
  isResolved: boolean;
  setAuth: (user: Principal) => void;
  setUnauthenticated: () => void;
  setResolutionFailed: () => void;
  setResolving: () => void;
  logout: () => void;
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      user: null,
      isAuthenticated: false,
      isResolved: false,
      setAuth: (user) => set({ user, isAuthenticated: true, isResolved: true }),
      setUnauthenticated: () => set({ user: null, isAuthenticated: false, isResolved: true }),
      setResolutionFailed: () => set({ user: null, isAuthenticated: false, isResolved: false }),
      setResolving: () => set({ isResolved: false }),
      logout: () => {
        set({ user: null, isAuthenticated: false, isResolved: true });
      },
    }),
    {
      name: AUTH_STORAGE_KEY,
      partialize: (state) => ({
        user: state.user,
        isAuthenticated: state.isAuthenticated,
      }),
    }
  )
);

/**
 * P5: 不再存储 token，仅持久化 user 信息和认证状态。
 * JWT token 通过 httpOnly cookie 传递，JavaScript 无法读取。
 */
export function getStoredAuthState(): { isAuthenticated: boolean; user: Principal | null } {
  if (typeof window === 'undefined') {
    return { isAuthenticated: false, user: null };
  }

  try {
    const raw = window.localStorage.getItem(AUTH_STORAGE_KEY);
    if (!raw) {
      return { isAuthenticated: false, user: null };
    }

    const parsed = JSON.parse(raw) as {
      state?: {
        isAuthenticated?: boolean;
        user?: Principal | null;
      };
    };

    return {
      isAuthenticated: parsed.state?.isAuthenticated ?? false,
      user: parsed.state?.user ?? null,
    };
  } catch {
    return { isAuthenticated: false, user: null };
  }
}
