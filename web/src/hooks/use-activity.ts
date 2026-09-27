import { useInfiniteQuery } from '@tanstack/react-query';
import type { InfiniteData, QueryClient, QueryKey } from '@tanstack/react-query';

import { activityApi, type ActivityReadScope } from '@/lib/api';
import { resourceScopeKey } from '@/lib/resource-scope';
import type {
  ActivityCategory,
  ActivityItem,
  ActivityPage,
  ActivityQuery,
  ActivitySeverity,
} from '@/types';

export interface NormalizedActivityQuery extends ActivityQuery {
  scope: 'global' | 'client' | 'tunnel';
  limit: number;
  severities: ActivitySeverity[];
  categories: ActivityCategory[];
}

function sortedUnique<T extends string>(values: T[] | undefined): T[] {
  return Array.from(new Set(values ?? [])).sort();
}

export function normalizeActivityQuery(query: ActivityQuery = {}): NormalizedActivityQuery {
  return {
    scope: query.scope ?? 'global',
    scopeId: query.scopeId,
    limit: Math.min(200, Math.max(1, query.limit ?? 50)),
    severities: sortedUnique(query.severities ?? ['info', 'warning', 'error']),
    categories: sortedUnique(query.categories),
    from: query.from,
    to: query.to,
  };
}

export function activityReadScopeKey(readScope: ActivityReadScope) {
  if (readScope.kind === 'admin-global') return 'admin-global';
  return resourceScopeKey(readScope);
}

function activityReadScopeUserFilter(readScope: ActivityReadScope) {
  return readScope.kind === 'admin-global' ? readScope.userId ?? null : null;
}

export function buildActivityQueryKey(readScope: ActivityReadScope, query: ActivityQuery = {}) {
  const normalized = normalizeActivityQuery(query);
  return [
    'users',
    activityReadScopeKey(readScope),
    'activity',
    activityReadScopeUserFilter(readScope),
    normalized.scope,
    normalized.scopeId ?? null,
    normalized.limit,
    normalized.severities,
    normalized.categories,
    normalized.from ?? null,
    normalized.to ?? null,
  ] as const;
}

export function useActivity(readScope: ActivityReadScope, query: ActivityQuery = {}) {
  const normalized = normalizeActivityQuery(query);
  const result = useInfiniteQuery({
    queryKey: buildActivityQueryKey(readScope, normalized),
    initialPageParam: undefined as number | undefined,
    queryFn: ({ pageParam }) => activityApi.list(readScope, { ...normalized, before: pageParam }),
    getNextPageParam: (page) => page.has_more ? page.next_cursor : undefined,
  });
  return {
    ...result,
    items: flattenActivityPages(result.data),
  };
}

export function flattenActivityPages(data: InfiniteData<ActivityPage> | undefined): ActivityItem[] {
  const byId = new Map<number, ActivityItem>();
  for (const page of data?.pages ?? []) {
    for (const item of page.items) byId.set(item.id, item);
  }
  return Array.from(byId.values()).sort((a, b) => b.id - a.id);
}

function activityQueryFromKey(queryKey: QueryKey): NormalizedActivityQuery | null {
  if (queryKey[0] !== 'users' || queryKey[2] !== 'activity' || typeof queryKey[4] !== 'string' || typeof queryKey[6] !== 'number') return null;
  return {
    scope: queryKey[4] as NormalizedActivityQuery['scope'],
    scopeId: typeof queryKey[5] === 'string' ? queryKey[5] : undefined,
    limit: queryKey[6],
    severities: Array.isArray(queryKey[7]) ? queryKey[7] as ActivitySeverity[] : [],
    categories: Array.isArray(queryKey[8]) ? queryKey[8] as ActivityCategory[] : [],
    from: typeof queryKey[9] === 'string' ? queryKey[9] : undefined,
    to: typeof queryKey[10] === 'string' ? queryKey[10] : undefined,
  };
}

export function activityMatchesQuery(item: ActivityItem, query: NormalizedActivityQuery) {
  if (query.scope === 'client' && !item.clients.some((subject) => subject.client_id === query.scopeId)) return false;
  if (query.scope === 'tunnel' && !item.tunnels.some((subject) => subject.tunnel_id === query.scopeId)) return false;
  if (query.severities.length > 0 && !query.severities.includes(item.severity)) return false;
  if (query.categories.length > 0 && !query.categories.includes(item.category)) return false;
  const occurredAt = Date.parse(item.occurred_at);
  if (query.from && occurredAt < Date.parse(query.from)) return false;
  if (query.to && occurredAt >= Date.parse(query.to)) return false;
  return true;
}

export function prependActivityToMatchingQueries(
  queryClient: QueryClient,
  readScope: ActivityReadScope,
  item: ActivityItem,
) {
  const scopeKey = activityReadScopeKey(readScope);
  // The global payload deliberately does not expose owner IDs.  A selected
  // global-user filter therefore cannot safely accept an incremental event;
  // refetch instead of risking a cross-user insertion.
  if (readScope.kind === 'admin-global' && readScope.userId) {
    void queryClient.invalidateQueries({ queryKey: ['users', scopeKey, 'activity'] });
    return;
  }
  for (const query of queryClient.getQueryCache().findAll({ queryKey: ['users', scopeKey, 'activity'] })) {
    const metadata = activityQueryFromKey(query.queryKey);
    if (!metadata || !activityMatchesQuery(item, metadata)) continue;
    queryClient.setQueryData<InfiniteData<ActivityPage>>(query.queryKey, (old) => {
      if (!old || old.pages.some((page) => page.items.some((entry) => entry.id === item.id))) return old;
      const pages = [...old.pages];
      const first = pages[0] ?? { items: [], has_more: false, direction: 'before' as const };
      pages[0] = { ...first, items: [item, ...first.items].sort((a, b) => b.id - a.id) };
      return { ...old, pages };
    });
  }
}
