import { createRoute, useParams } from '@tanstack/react-router';

import { ClientDetailPage } from '@/routes/dashboard/clients.$clientId';
import { requireAdmin } from '@/lib/auth';
import { adminUserResourceScope } from '@/lib/resource-scope';
import { dashboardRoute } from '@/routes/dashboard';

function ManagedUserClientDetailRoute() {
  const { userId, clientId } = useParams({ from: '/dashboard/users/$userId/clients/$clientId' });
  return <ClientDetailPage scope={adminUserResourceScope(userId)} clientId={clientId} />;
}

export const dashboardUserClientRoute = createRoute({
  getParentRoute: () => dashboardRoute,
  path: '/users/$userId/clients/$clientId',
  beforeLoad: requireAdmin,
  component: ManagedUserClientDetailRoute,
});
