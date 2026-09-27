import { createRoute, useNavigate, useParams } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';

import { OverviewPage, type DashboardTab } from '@/components/custom/dashboard/OverviewPage';
import { Skeleton } from '@/components/ui/skeleton';
import { useManagedUser } from '@/hooks/use-users';
import { requireAdmin } from '@/lib/auth';
import { adminUserResourceScope } from '@/lib/resource-scope';
import { dashboardRoute } from '@/routes/dashboard';

type UserWorkspaceSearch = { tab?: DashboardTab };

function normalizeUserWorkspaceSearch(search: Record<string, unknown>): UserWorkspaceSearch {
  const tab = search.tab;
  return tab === 'topology' || tab === 'clients' || tab === 'tunnels' ? { tab } : {};
}

function UserWorkspacePage() {
  const { t } = useTranslation();
  const { userId } = useParams({ from: '/dashboard/users/$userId' });
  const search = dashboardUserWorkspaceRoute.useSearch();
  const navigate = useNavigate({ from: dashboardUserWorkspaceRoute.fullPath });
  const user = useManagedUser(userId);
  const scope = adminUserResourceScope(userId);

  if (user.isLoading) {
    return (
      <div className="z-10 mx-auto flex w-full max-w-6xl flex-col gap-5 p-4 sm:gap-6 sm:p-6 lg:p-8">
        <Skeleton className="h-9 w-64" />
        <Skeleton className="h-72 w-full rounded-xl" />
      </div>
    );
  }

  if (user.isError || !user.data) {
    return (
      <div className="z-10 mx-auto w-full max-w-6xl p-4 text-sm text-destructive sm:p-6 lg:p-8">
        {user.error instanceof Error ? user.error.message : t('users.notFound')}
      </div>
    );
  }

  return (
    <>
      <div className="z-10 mx-auto w-full max-w-6xl px-4 pt-4 sm:px-6 sm:pt-6 lg:px-8 lg:pt-8">
        <p className="text-sm text-muted-foreground">{t('users.workspace')}</p>
        <div className="mt-1 flex flex-wrap items-center gap-2">
          <h1 className="text-2xl font-semibold tracking-tight">{user.data.username}</h1>
          <span className="rounded-full bg-muted px-2 py-0.5 text-xs text-muted-foreground">
            {user.data.is_admin ? t('users.admin') : t('users.member')}
          </span>
          <span className="rounded-full bg-muted px-2 py-0.5 text-xs text-muted-foreground">
            {user.data.status === 'active' ? t('users.active') : t('users.disabled')}
          </span>
        </div>
      </div>
      <OverviewPage
        scope={scope}
        tab={search.tab}
        onTabChange={(tab) => {
          navigate({ search: { tab }, replace: true });
        }}
      />
    </>
  );
}

export const dashboardUserWorkspaceRoute = createRoute({
  getParentRoute: () => dashboardRoute,
  path: '/users/$userId',
  validateSearch: normalizeUserWorkspaceSearch,
  beforeLoad: requireAdmin,
  component: UserWorkspacePage,
});
