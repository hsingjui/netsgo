import { createRoute } from '@tanstack/react-router';

import { UserListPage } from '@/components/custom/users/UserListPage';
import { requireAdmin } from '@/lib/auth';
import { dashboardRoute } from '@/routes/dashboard';

export const dashboardUsersRoute = createRoute({
  getParentRoute: () => dashboardRoute,
  path: '/users',
  beforeLoad: requireAdmin,
  component: UserListPage,
});
