import { createRoute, useNavigate } from '@tanstack/react-router';
import { motion } from 'motion/react';
import { useTranslation } from 'react-i18next';

import { ActivityFilters } from '@/components/custom/activity/ActivityFilters';
import type { ActivityFilterValue } from '@/components/custom/activity/severity-meta';
import { ActivityTimeline } from '@/components/custom/activity/ActivityTimeline';
import { dashboardRoute } from '@/routes/dashboard';
import { requireConsoleAuth } from '@/lib/auth';
import { adminUserResourceScope, SELF_RESOURCE_SCOPE } from '@/lib/resource-scope';
import { useAuthStore } from '@/stores/auth-store';
import { useAllUsers } from '@/hooks/use-users';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import type { ActivityCategory, ActivityScope, ActivitySeverity } from '@/types';

const severityValues = new Set<ActivitySeverity>(['debug', 'info', 'warning', 'error']);
const categoryValues = new Set<ActivityCategory>(['client', 'tunnel', 'p2p', 'admin', 'security']);
const defaultSeverities: ActivitySeverity[] = ['info', 'warning', 'error'];

export interface ActivitySearch {
  scope: ActivityScope;
  client_id?: string;
  tunnel_id?: string;
  user_id?: string;
  severity: ActivitySeverity[];
  category: ActivityCategory[];
  from?: string;
  to?: string;
}

function strings(value: unknown) {
  if (Array.isArray(value)) return value.filter((entry): entry is string => typeof entry === 'string');
  return typeof value === 'string' ? [value] : [];
}

function validDate(value: unknown) {
  return typeof value === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(value) && Number.isFinite(Date.parse(`${value}T00:00:00`)) ? value : undefined;
}

export function normalizeActivitySearch(search: Record<string, unknown>): ActivitySearch {
  const rawScope = search.scope;
  const clientId = typeof search.client_id === 'string' && search.client_id.trim() ? search.client_id : undefined;
  const tunnelId = typeof search.tunnel_id === 'string' && search.tunnel_id.trim() ? search.tunnel_id : undefined;
  const scope: ActivityScope = rawScope === 'client' && clientId
    ? 'client'
    : rawScope === 'tunnel' && tunnelId
      ? 'tunnel'
      : 'global';
  const severity = [...new Set(strings(search.severity).filter((entry): entry is ActivitySeverity => severityValues.has(entry as ActivitySeverity)))];
  const category = [...new Set(strings(search.category).filter((entry): entry is ActivityCategory => categoryValues.has(entry as ActivityCategory)))];
  return {
    scope,
    client_id: scope === 'client' ? clientId : undefined,
    tunnel_id: scope === 'tunnel' ? tunnelId : undefined,
    user_id: typeof search.user_id === 'string' && search.user_id.trim() ? search.user_id : undefined,
    severity: severity.length > 0 ? severity : defaultSeverities,
    category,
    from: validDate(search.from),
    to: validDate(search.to),
  };
}

function dateBoundary(value: string | undefined, nextDay = false) {
  if (!value) return undefined;
  const date = new Date(`${value}T00:00:00`);
  if (nextDay) date.setDate(date.getDate() + 1);
  return date.toISOString();
}

function ActivityPage() {
  const { t } = useTranslation();
  const principal = useAuthStore((state) => state.user);
  const search = dashboardActivityRoute.useSearch();
  const navigate = useNavigate({ from: dashboardActivityRoute.fullPath });
  const isAdmin = principal?.is_admin === true;
  const users = useAllUsers({ enabled: isAdmin });
  // A selected user switches to the explicit target-user endpoint and SSE
  // scope.  That prevents a global stream event from being inserted into a
  // user-filtered cache before the browser can establish its ownership.
  const readScope = isAdmin
    ? search.user_id
      ? adminUserResourceScope(search.user_id)
      : { kind: 'admin-global' as const }
    : SELF_RESOURCE_SCOPE;
  const scopeId = search.scope === 'client' ? search.client_id : search.scope === 'tunnel' ? search.tunnel_id : undefined;
  const query = {
    scope: search.scope,
    scopeId,
    limit: 50,
    severities: search.severity,
    categories: search.category,
    from: dateBoundary(search.from),
    to: dateBoundary(search.to, true),
  };
  const filters: ActivityFilterValue = {
    severities: search.severity,
    categories: search.category,
    fromDate: search.from,
    toDate: search.to,
  };
  const updateFilters = (next: ActivityFilterValue) => {
    navigate({
      search: (current) => ({
        ...current,
        severity: next.severities.length > 0 ? next.severities : defaultSeverities,
        category: next.categories,
        from: next.fromDate,
        to: next.toDate,
      }),
      replace: true,
    });
  };

  return (
    <motion.div
      variants={{ hidden: {}, show: { transition: { staggerChildren: 0.08 } } }}
      initial="hidden"
      animate="show"
      className="z-10 mx-auto flex w-full max-w-6xl flex-col gap-5 p-4 sm:gap-6 sm:p-6 lg:p-8"
    >
      <motion.div variants={fadeUp} className="flex flex-col gap-3">
        <div>
          <h3 className="text-xl font-semibold tracking-tight">{t('activity.pageTitle')}</h3>
          <p className="mt-1 text-sm text-muted-foreground">{t('activity.pageDescription')}</p>
        </div>
      </motion.div>
      <motion.div variants={fadeUp}>
        <section className="rounded-xl border border-border/40 bg-card/50 shadow-sm backdrop-blur-sm">
          <header className="rounded-t-xl border-b border-border/40 bg-muted/20 px-3 py-2.5 sm:px-4">
            <ActivityFilters
              value={filters}
              onChange={updateFilters}
              auxiliaryFilters={isAdmin ? (
                <Select
                  value={search.user_id ?? '__all__'}
                  onValueChange={(value) => {
                    navigate({
                      search: (current) => ({
                        ...current,
                        user_id: value === '__all__' ? undefined : value,
                      }),
                      replace: true,
                    });
                  }}
                >
                  <SelectTrigger size="sm" className="h-7 w-full text-xs shadow-none">
                    <SelectValue placeholder={t('activity.allUsers')} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="__all__">{t('activity.allUsers')}</SelectItem>
                    {(users.data ?? []).map((user) => (
                      <SelectItem key={user.id} value={user.id}>{user.username}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              ) : undefined}
            />
          </header>
          <ActivityTimeline readScope={readScope} query={query} />
        </section>
      </motion.div>
    </motion.div>
  );
}

const fadeUp = {
  hidden: { opacity: 0, y: 12 },
  show: { opacity: 1, y: 0, transition: { duration: 0.35, ease: 'easeOut' as const } },
};

export const dashboardActivityRoute = createRoute({
  getParentRoute: () => dashboardRoute,
  path: '/activity',
  validateSearch: normalizeActivitySearch,
  beforeLoad: requireConsoleAuth,
  component: ActivityPage,
});
