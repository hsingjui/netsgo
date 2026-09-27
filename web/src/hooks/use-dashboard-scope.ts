import { useRouterState } from '@tanstack/react-router';

import { adminUserResourceScope, SELF_RESOURCE_SCOPE, type ResourceScope } from '@/lib/resource-scope';
import { useAuthStore } from '@/stores/auth-store';

/**
 * Resource-bearing dashboard routes carry their owner in the path.  An
 * administrator on the user list, activity log, or global settings has no
 * implicit resource scope and therefore never triggers a page-level resource
 * query. The sidebar has a separate scope below so an administrator can keep
 * navigating their own clients without changing the page's data scope.
 */
export function useDashboardResourceScope(): ResourceScope | null {
  const principal = useAuthStore((state) => state.user);
  const pathname = useRouterState({ select: (state) => state.location.pathname });

  const targetMatch = /^\/dashboard\/users\/([^/]+)(?:\/|$)/.exec(pathname);
  if (targetMatch) {
    return adminUserResourceScope(decodeURIComponent(targetMatch[1]));
  }
  if (pathname === '/dashboard') {
    return SELF_RESOURCE_SCOPE;
  }
  if (!principal?.is_admin && pathname.startsWith('/dashboard')) {
    return SELF_RESOURCE_SCOPE;
  }
  return null;
}

/**
 * The sidebar always needs a concrete owner for its client list. An admin's
 * global pages use the admin's own resources in the sidebar, while an explicit
 * user workspace keeps using the selected user's resources.
 */
export function useDashboardSidebarScope(): ResourceScope | null {
  const principal = useAuthStore((state) => state.user);
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  const resourceScope = useDashboardResourceScope();

  if (resourceScope) {
    return resourceScope;
  }
  if (principal?.is_admin && pathname.startsWith('/dashboard')) {
    return SELF_RESOURCE_SCOPE;
  }
  return null;
}

export function useIsUserManagementRoute() {
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  return pathname === '/dashboard/users' || pathname.startsWith('/dashboard/users/');
}
