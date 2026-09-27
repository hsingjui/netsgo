import { createRoute } from '@tanstack/react-router';
import { dashboardRoute } from '@/routes/dashboard';
import { OverviewPage } from '@/components/custom/dashboard/OverviewPage';
import { requireConsoleAuth } from '@/lib/auth';
import { SELF_RESOURCE_SCOPE } from '@/lib/resource-scope';

function DashboardIndexPage() {
  return <OverviewPage scope={SELF_RESOURCE_SCOPE} />;
}

export const dashboardIndexRoute = createRoute({
  getParentRoute: () => dashboardRoute,
  path: '/',
  beforeLoad: requireConsoleAuth,
  component: DashboardIndexPage,
});
