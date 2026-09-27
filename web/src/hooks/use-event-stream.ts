import { useEffect, useMemo } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import type { QueryClient } from '@tanstack/react-query';
import { useRouterState } from '@tanstack/react-router';
import { activityApi, api, scopedConsoleSnapshotPath, scopedEventStreamPath, type ActivityReadScope } from '@/lib/api';
import { useConnectionStore } from '@/stores/connection-store';
import type { ConnectionStatus } from '@/stores/connection-store';
import { useAuthStore } from '@/stores/auth-store';
import { clearClientSessionAndRedirect } from '@/lib/session';
import { useDashboardResourceScope, useDashboardSidebarScope } from '@/hooks/use-dashboard-scope';
import { buildClientTrafficQueryKey } from '@/hooks/use-client-traffic';
import { activityReadScopeKey, prependActivityToMatchingQueries } from '@/hooks/use-activity';
import { resourceScopeKey, scopedQueryKey, SELF_RESOURCE_SCOPE, type ResourceScope } from '@/lib/resource-scope';
import { isResourceBootstrap } from '@/lib/resource-bootstrap';
import type {
  Client,
  ActivityItem,
  ActivityPage,
  ClientOfflineEvent,
  ClientOnlineEvent,
  ClientTrafficResponse,
  ConsoleSnapshot,
  ConsoleSummary,
  ProxyConfig,
  ResourceBootstrap,
  StatsUpdateEvent,
  TunnelChangedEvent,
  TrafficRealtimeEvent,
} from '@/types';

type EventStreamQueryClient = QueryClient;
type JsonObject = Record<string, unknown>;

interface UserListChangedEvent {
  user_id: string;
}

interface WebhookChangedEvent {
  webhook_id: string;
}

interface WebhookDeliveryChangedEvent extends WebhookChangedEvent {
  delivery_id: string;
  status: string;
}

export interface EventStreamDiagnostics {
  eventType: string;
  action?: string;
  clientId?: string;
  tunnelId?: string;
  tunnelName?: string;
  runtimeState?: string;
  desiredState?: string;
  snapshotRequestId?: number;
  snapshotGeneratedAt?: string;
  tunnels?: string[];
}

export interface EventStreamSnapshotState {
  requestSeq: number;
  appliedGeneratedAt?: number;
}

export interface ActivityRecoveryState {
  lastScannedId?: number;
  targetId: number;
  hints: Map<number, ActivityItem>;
  running: boolean;
  retryTimer?: ReturnType<typeof setTimeout>;
  retryAttempt: number;
  cancelled: boolean;
}

const activityHintBufferLimit = 256;
const activityRecoveryRetryDelays = [1000, 2000, 5000, 10000] as const;

export function createActivityRecoveryState(): ActivityRecoveryState {
  return { targetId: 0, hints: new Map(), running: false, retryAttempt: 0, cancelled: false };
}

const consoleSummaryFields = [
  'total_clients',
  'online_clients',
  'offline_clients',
  'total_tunnels',
  'active_tunnels',
  'inactive_tunnels',
  'pending_tunnels',
  'offline_tunnels',
  'stopped_tunnels',
  'error_tunnels',
] as const satisfies readonly (keyof ConsoleSummary)[];
export function createEventStreamSnapshotState(): EventStreamSnapshotState {
  return { requestSeq: 0 };
}

function isRecord(value: unknown): value is JsonObject {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0;
}

function isUserListChangedEvent(value: unknown): value is UserListChangedEvent {
  return isRecord(value) && isNonEmptyString(value.user_id);
}

function isWebhookChangedEvent(value: unknown): value is WebhookChangedEvent {
  return isRecord(value) && isNonEmptyString(value.webhook_id);
}

function isWebhookDeliveryChangedEvent(value: unknown): value is WebhookDeliveryChangedEvent {
  return isRecord(value)
    && isNonEmptyString(value.webhook_id)
    && isNonEmptyString(value.delivery_id)
    && isNonEmptyString(value.status);
}

function isConsoleSummary(value: unknown): value is ConsoleSummary {
  return isRecord(value) && consoleSummaryFields.every((field) => typeof value[field] === 'number');
}

function isClient(value: unknown): value is Client {
  return (
    isRecord(value) &&
    isNonEmptyString(value.id) &&
    typeof value.ingress_bps === 'number' &&
    typeof value.egress_bps === 'number' &&
    isRecord(value.info) &&
    (value.stats === undefined || value.stats === null || isRecord(value.stats)) &&
    (value.proxies === undefined || Array.isArray(value.proxies)) &&
    typeof value.online === 'boolean'
  );
}

function isConsoleSnapshot(value: unknown): value is ConsoleSnapshot {
  return (
    isRecord(value) &&
    Array.isArray(value.clients) && value.clients.every(isClient) &&
    isConsoleSummary(value.summary) &&
    isResourceBootstrap(value.bootstrap) &&
    typeof value.generated_at === 'string' &&
    typeof value.fresh_until === 'string'
  );
}

function isStatsUpdateEvent(value: unknown): value is StatsUpdateEvent {
  return (
    isRecord(value) &&
    isNonEmptyString(value.client_id) &&
    isRecord(value.stats)
  );
}

function isTrafficResolution(value: unknown): value is ClientTrafficResponse['resolution'] {
  return value === 'second' || value === 'minute' || value === 'hour';
}

function isTrafficPoint(value: unknown): boolean {
  return (
    isRecord(value) &&
    typeof value.bucket_start === 'string' &&
    typeof value.ingress_bytes === 'number' &&
    typeof value.egress_bytes === 'number' &&
    typeof value.total_bytes === 'number'
  );
}

function isTunnelTrafficSeries(value: unknown): boolean {
  return isRecord(value) && Array.isArray(value.points) && value.points.every(isTrafficPoint);
}

function isTrafficRealtimeClient(value: unknown): value is TrafficRealtimeEvent['clients'][number] {
  return (
    isRecord(value) &&
    isNonEmptyString(value.client_id) &&
    isTrafficResolution(value.resolution) &&
    Array.isArray(value.items) &&
    value.items.every(isTunnelTrafficSeries)
  );
}

function isTrafficRealtimeEvent(value: unknown): value is TrafficRealtimeEvent {
  return (
    isRecord(value) &&
    (value.generated_at === undefined || typeof value.generated_at === 'string') &&
    Array.isArray(value.clients) &&
    value.clients.every(isTrafficRealtimeClient)
  );
}

function isClientOnlineEvent(value: unknown): value is ClientOnlineEvent {
  return isRecord(value) && isNonEmptyString(value.client_id) && isRecord(value.info);
}

function isClientOfflineEvent(value: unknown): value is ClientOfflineEvent {
  return isRecord(value) && isNonEmptyString(value.client_id);
}

function isProxyConfig(value: unknown): value is ProxyConfig {
  return (
    isRecord(value) &&
    isNonEmptyString(value.id) &&
    isNonEmptyString(value.name) &&
    isNonEmptyString(value.type) &&
    typeof value.local_ip === 'string' &&
    typeof value.local_port === 'number' &&
    typeof value.remote_port === 'number' &&
    typeof value.domain === 'string' &&
    isNonEmptyString(value.client_id) &&
    typeof value.ingress_bps === 'number' &&
    typeof value.egress_bps === 'number' &&
    typeof value.created_at === 'string' &&
    isNonEmptyString(value.desired_state) &&
    isNonEmptyString(value.runtime_state) &&
    (value.capabilities === undefined || isRecord(value.capabilities))
  );
}
interface ActivityReadyPayload {
  activity_cursor: number;
}

function isActivityReadyPayload(value: unknown): value is ActivityReadyPayload {
  return isRecord(value) && Number.isSafeInteger(value.activity_cursor) && Number(value.activity_cursor) >= 0;
}

function isActivityItem(value: unknown): value is ActivityItem {
  return isRecord(value)
    && Number.isSafeInteger(value.id) && Number(value.id) > 0
    && typeof value.occurred_at === 'string'
    && typeof value.recorded_at === 'string'
    && ['debug', 'info', 'warning', 'error'].includes(String(value.severity))
    && ['client', 'tunnel', 'p2p', 'admin', 'security'].includes(String(value.category))
    && typeof value.action === 'string'
    && typeof value.source === 'string'
    && isRecord(value.actor)
    && Number.isSafeInteger(value.payload_version)
    && isRecord(value.payload)
    && Array.isArray(value.clients)
    && Array.isArray(value.tunnels);
}


function isTunnelChangedEvent(value: unknown): value is TunnelChangedEvent {
  return (
    isRecord(value) &&
    isNonEmptyString(value.client_id) &&
    (value.action === undefined || typeof value.action === 'string') &&
    isProxyConfig(value.tunnel)
  );
}

function parseEventPayload<T>(data: string, guard: (value: unknown) => value is T): T | null {
  try {
    const parsed: unknown = JSON.parse(data);
    return guard(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

function isEventStreamDebugEnabled() {
  try {
    return localStorage.getItem('netsgo:debug:event-stream') === '1';
  } catch {
    return false;
  }
}

function logEventStreamDiagnostic(stage: string, diagnostic: EventStreamDiagnostics) {
  if (!isEventStreamDebugEnabled()) {
    return;
  }
  console.debug('[netsgo:event-stream]', stage, diagnostic);
}

function summarizeClientTunnelStates(clients: Client[] | undefined) {
  return clients?.flatMap((client) =>
    (client.proxies ?? []).map((proxy) => `${client.id}/${proxy.name}:${proxy.runtime_state}`),
  );
}

function snapshotGeneratedAtMillis(snapshot: ConsoleSnapshot) {
  if (!snapshot.generated_at) {
    return undefined;
  }
  const generatedAt = Date.parse(snapshot.generated_at);
  return Number.isFinite(generatedAt) ? generatedAt : undefined;
}

function snapshotDiagnostic(eventType: string, snapshot: ConsoleSnapshot, snapshotRequestId?: number): EventStreamDiagnostics {
  return {
    eventType,
    clientId: snapshot.clients?.[0]?.id,
    snapshotRequestId,
    snapshotGeneratedAt: snapshot.generated_at,
    tunnels: summarizeClientTunnelStates(snapshot.clients),
  };
}

function applyConsoleSnapshot(
  queryClient: EventStreamQueryClient,
  scope: ResourceScope,
  snapshotState: EventStreamSnapshotState,
  snapshot: ConsoleSnapshot,
) {
  const generatedAt = snapshotGeneratedAtMillis(snapshot);
  if (generatedAt !== undefined) {
    const appliedGeneratedAt = snapshotState.appliedGeneratedAt;
    if (appliedGeneratedAt !== undefined && generatedAt < appliedGeneratedAt) {
      return false;
    }
    snapshotState.appliedGeneratedAt = generatedAt;
  }

  queryClient.setQueryData<Client[]>(scopedQueryKey(scope, 'clients'), snapshot.clients);
  queryClient.setQueryData<ConsoleSummary>(scopedQueryKey(scope, 'console-summary'), snapshot.summary);
  queryClient.setQueryData<ResourceBootstrap>(scopedQueryKey(scope, 'resource-bootstrap'), snapshot.bootstrap);
  return true;
}

async function resyncConsoleSnapshot(
  queryClient: EventStreamQueryClient,
  scope: ResourceScope,
  snapshotState: EventStreamSnapshotState,
) {
  const snapshotRequestId = ++snapshotState.requestSeq;
  logEventStreamDiagnostic('snapshot_request_start', { eventType: 'snapshot_request', snapshotRequestId });
  let snapshot: ConsoleSnapshot;
  try {
    const response = await api.get<unknown>(scopedConsoleSnapshotPath(scope));
    if (!isConsoleSnapshot(response)) {
      throw new Error('invalid console snapshot response');
    }
    snapshot = response;
  } catch (error) {
    if (snapshotRequestId !== snapshotState.requestSeq) {
      logEventStreamDiagnostic('snapshot_request_stale', { eventType: 'snapshot_request', snapshotRequestId });
      return false;
    }
    throw error;
  }
  const diagnostic = snapshotDiagnostic('snapshot_request', snapshot, snapshotRequestId);
  if (snapshotRequestId !== snapshotState.requestSeq) {
    logEventStreamDiagnostic('snapshot_request_stale', diagnostic);
    return false;
  }
  const applied = applyConsoleSnapshot(queryClient, scope, snapshotState, snapshot);
  logEventStreamDiagnostic(applied ? 'snapshot_request_apply' : 'snapshot_request_stale', diagnostic);
  return applied;
}

function invalidateConsoleSnapshotQueries(queryClient: EventStreamQueryClient, scope: ResourceScope) {
  queryClient.invalidateQueries({ queryKey: ['users', resourceScopeKey(scope)] });
}

function resyncConsoleSnapshotSafely(
  queryClient: EventStreamQueryClient,
  scope: ResourceScope,
  snapshotState: EventStreamSnapshotState,
  setStatus?: (status: ConnectionStatus) => void,
) {
  return resyncConsoleSnapshot(queryClient, scope, snapshotState)
    .then((applied) => {
      if (applied) {
        setStatus?.('connected');
      }
    })
    .catch((error) => {
      console.warn('Failed to resync console snapshot:', error);
      invalidateConsoleSnapshotQueries(queryClient, scope);
      setStatus?.('reconnecting');
    });
}

function applyRealtimeTraffic(
  queryClient: EventStreamQueryClient,
  scope: ResourceScope,
  client: TrafficRealtimeEvent['clients'][number],
) {
  const traffic: ClientTrafficResponse = {
    resolution: client.resolution,
    items: client.items ?? [],
  };
  const baseKey = buildClientTrafficQueryKey(scope, client.client_id, '60s');
  queryClient.setQueryData<ClientTrafficResponse>(baseKey, traffic);

  const realtimeQueries = queryClient.getQueryCache().findAll({
    queryKey: scopedQueryKey(scope, 'client-traffic', client.client_id, '60s'),
  });
  for (const query of realtimeQueries) {
    const tunnelName = typeof query.queryKey[5] === 'string' ? query.queryKey[5] : '';
    queryClient.setQueryData<ClientTrafficResponse>(
      query.queryKey,
      tunnelName
        ? { ...traffic, items: traffic.items.filter((item) => item.tunnel_name === tunnelName) }
        : traffic,
    );
  }
}

function getTunnelChangedClientIds(event: TunnelChangedEvent) {
  return Array.from(new Set([
    event.client_id,
    event.tunnel.owner_client_id,
    event.tunnel.ingress?.client_id,
    event.tunnel.target?.client_id,
    event.tunnel.client_id,
  ].filter((clientId): clientId is string => Boolean(clientId))));
}

function invalidateActivityQueries(queryClient: EventStreamQueryClient, readScope: ActivityReadScope) {
  queryClient.invalidateQueries({ queryKey: ['users', activityReadScopeKey(readScope), 'activity'] });
}

function scheduleActivityRecoveryRetry(
  queryClient: EventStreamQueryClient,
  readScope: ActivityReadScope,
  state: ActivityRecoveryState,
) {
  if (state.cancelled || state.retryTimer) return;
  const delay = activityRecoveryRetryDelays[Math.min(state.retryAttempt, activityRecoveryRetryDelays.length - 1)];
  state.retryAttempt += 1;
  state.retryTimer = setTimeout(() => {
    state.retryTimer = undefined;
    void recoverActivityGap(queryClient, readScope, state);
  }, delay);
}

export async function recoverActivityGap(
  queryClient: EventStreamQueryClient,
  readScope: ActivityReadScope,
  state: ActivityRecoveryState,
) {
  if (state.cancelled || state.running || state.lastScannedId === undefined || state.targetId <= state.lastScannedId) return;
  state.running = true;
  try {
    while (!state.cancelled && state.lastScannedId !== undefined && state.targetId > state.lastScannedId) {
      const targetAtStart = state.targetId;
      const page: ActivityPage = await activityApi.recovery(readScope, state.lastScannedId);
      for (const item of [...page.items].reverse()) {
        prependActivityToMatchingQueries(queryClient, readScope, item);
        state.hints.delete(item.id);
      }
      if (page.next_cursor && page.next_cursor > state.lastScannedId) {
        state.lastScannedId = page.next_cursor;
      } else if (!page.has_more) {
        state.lastScannedId = targetAtStart;
      } else {
        throw new Error('activity recovery cursor did not advance');
      }
      if (!page.has_more && state.lastScannedId < targetAtStart) state.lastScannedId = targetAtStart;
    }
    state.retryAttempt = 0;
    for (const [id, hint] of state.hints) {
      if (state.lastScannedId !== undefined && id <= state.lastScannedId) {
        prependActivityToMatchingQueries(queryClient, readScope, hint);
        state.hints.delete(id);
      }
    }
  } catch (error) {
    console.warn('Failed to recover activity gap:', error);
    scheduleActivityRecoveryRetry(queryClient, readScope, state);
  } finally {
    state.running = false;
    if (!state.cancelled && !state.retryTimer && state.lastScannedId !== undefined && state.targetId > state.lastScannedId) {
      void recoverActivityGap(queryClient, readScope, state);
    }
  }
}

function applyActivityReady(
  queryClient: EventStreamQueryClient,
  readScope: ActivityReadScope,
  state: ActivityRecoveryState,
  ready: ActivityReadyPayload,
) {
  if (state.lastScannedId === undefined) {
    state.lastScannedId = ready.activity_cursor;
    state.targetId = Math.max(state.targetId, ready.activity_cursor);
    for (const id of state.hints.keys()) if (id <= ready.activity_cursor) state.hints.delete(id);
    invalidateActivityQueries(queryClient, readScope);
    return;
  }
  state.targetId = Math.max(state.targetId, ready.activity_cursor);
  void recoverActivityGap(queryClient, readScope, state);
}

function applyActivityHint(
  queryClient: EventStreamQueryClient,
  readScope: ActivityReadScope,
  state: ActivityRecoveryState,
  item: ActivityItem,
) {
  prependActivityToMatchingQueries(queryClient, readScope, item);
  state.targetId = Math.max(state.targetId, item.id);
  if (state.hints.size >= activityHintBufferLimit && !state.hints.has(item.id)) {
    state.hints.clear();
    invalidateActivityQueries(queryClient, readScope);
  } else {
    state.hints.set(item.id, item);
  }
  if (state.retryTimer) {
    clearTimeout(state.retryTimer);
    state.retryTimer = undefined;
  }
  void recoverActivityGap(queryClient, readScope, state);
}

export function applyEventForDiagnostics(
  queryClient: EventStreamQueryClient,
  setStatus: (status: ConnectionStatus) => void,
  snapshotState: EventStreamSnapshotState,
  eventType: string,
  data: string,
  activityState?: ActivityRecoveryState,
  readScope: ActivityReadScope = SELF_RESOURCE_SCOPE,
) {
  const resourceScope = readScope.kind === 'admin-global' ? null : readScope;
  switch (eventType) {
    case 'ready': {
      if (!activityState) return;
      const parsed = parseEventPayload(data, isActivityReadyPayload);
      if (!parsed) {
        invalidateActivityQueries(queryClient, readScope);
        return;
      }
      applyActivityReady(queryClient, readScope, activityState, parsed);
      return;
    }
    case 'activity_event': {
      if (!activityState) return;
      const parsed = parseEventPayload(data, isActivityItem);
      if (!parsed) {
        invalidateActivityQueries(queryClient, readScope);
        return;
      }
      applyActivityHint(queryClient, readScope, activityState, parsed);
      return;
    }
    case 'user_list_changed': {
      if (readScope.kind !== 'admin-global') return;
      void queryClient.invalidateQueries({ queryKey: ['admin-users'] });
      const parsed = parseEventPayload(data, isUserListChangedEvent);
      void queryClient.invalidateQueries({
        queryKey: parsed ? ['admin-user', parsed.user_id] : ['admin-user'],
      });
      return;
    }
    case 'webhook_changed': {
      if (!parseEventPayload(data, isWebhookChangedEvent)) return;
      void queryClient.invalidateQueries({ queryKey: ['webhooks'] });
      return;
    }
    case 'webhook_delivery_changed': {
      const parsed = parseEventPayload(data, isWebhookDeliveryChangedEvent);
      if (!parsed) return;
      void queryClient.invalidateQueries({ queryKey: ['webhook-deliveries', parsed.webhook_id] });
      void queryClient.invalidateQueries({ queryKey: ['webhook-delivery', parsed.delivery_id] });
      void queryClient.invalidateQueries({ queryKey: ['webhooks'] });
      return;
    }
    case 'snapshot': {
      if (!resourceScope) return;
      const parsed = parseEventPayload(data, isConsoleSnapshot);
      if (parsed) {
        const applied = applyConsoleSnapshot(queryClient, resourceScope, snapshotState, parsed);
        logEventStreamDiagnostic(applied ? 'sse_snapshot_apply' : 'sse_snapshot_stale', snapshotDiagnostic(eventType, parsed));
      }
      return;
    }
    case 'stats_update': {
      if (!resourceScope) return;
      const parsed = parseEventPayload(data, isStatsUpdateEvent);
      if (parsed) {
        queryClient.setQueryData<Client[]>(scopedQueryKey(resourceScope, 'clients'), (old) =>
          old?.map((client) =>
            client.id === parsed.client_id ? { ...client, stats: parsed.stats } : client,
          ),
        );
      }
      return;
    }
    case 'traffic_realtime': {
      if (!resourceScope) return;
      const parsed = parseEventPayload(data, isTrafficRealtimeEvent);
      if (!parsed) {
        return;
      }
      for (const client of parsed.clients) {
        applyRealtimeTraffic(queryClient, resourceScope, client);
      }
      return;
    }
    case 'client_online':
      {
        if (!resourceScope) return;
        const parsed = parseEventPayload(data, isClientOnlineEvent);
        if (!parsed) {
          queryClient.invalidateQueries({ queryKey: scopedQueryKey(resourceScope, 'clients') });
          return;
        }
        const info = parsed.info as Client['info'];
        queryClient.setQueryData<Client[]>(scopedQueryKey(resourceScope, 'clients'), (old) => {
          const base = old ?? [];
          const exists = base.some((client) => client.id === parsed.client_id);
          if (!exists) {
            return [
              ...base,
              {
                id: parsed.client_id,
                ingress_bps: 0,
                egress_bps: 0,
                info,
                stats: null,
                proxies: [],
                online: true,
              },
            ];
          }
          return base.map((client) =>
            client.id === parsed.client_id ? { ...client, info, online: true } : client,
          );
        });
        void resyncConsoleSnapshotSafely(queryClient, resourceScope, snapshotState, setStatus);
      }
      return;
    case 'client_offline':
      {
        if (!resourceScope) return;
        const parsed = parseEventPayload(data, isClientOfflineEvent);
        if (!parsed) {
          queryClient.invalidateQueries({ queryKey: scopedQueryKey(resourceScope, 'clients') });
          return;
        }
        queryClient.setQueryData<Client[]>(scopedQueryKey(resourceScope, 'clients'), (old) =>
          old?.map((client) =>
            client.id === parsed.client_id ? { ...client, online: false } : client,
          ),
        );
        void resyncConsoleSnapshotSafely(queryClient, resourceScope, snapshotState, setStatus);
      }
      return;
    case 'tunnel_changed':
      {
        if (!resourceScope) return;
        const parsed = parseEventPayload(data, isTunnelChangedEvent);
        if (!parsed) {
          logEventStreamDiagnostic('tunnel_changed_invalid', { eventType });
          queryClient.invalidateQueries({ queryKey: scopedQueryKey(resourceScope, 'clients') });
          return;
        }
        logEventStreamDiagnostic('tunnel_changed_apply', {
          eventType,
          action: parsed.action,
          clientId: parsed.client_id,
          tunnelId: parsed.tunnel.id,
          tunnelName: parsed.tunnel.name,
          runtimeState: parsed.tunnel.runtime_state,
          desiredState: parsed.tunnel.desired_state,
        });
        const migratedOut = parsed.action === 'migrated_out';
        const relatedClientIds = migratedOut ? [parsed.client_id] : getTunnelChangedClientIds(parsed);
        queryClient.setQueryData<Client[]>(scopedQueryKey(resourceScope, 'clients'), (old) =>
          old?.map((client) => {
            if (!relatedClientIds.includes(client.id)) {
              return client;
            }

            const proxies = client.proxies ?? [];
            if (parsed.action === 'deleted' || migratedOut) {
              return {
                ...client,
                proxies: proxies.filter((proxy) => proxy.id !== parsed.tunnel.id),
              };
            }

            const existingIndex = proxies.findIndex((proxy) => proxy.id === parsed.tunnel.id);
            if (existingIndex === -1) {
              return {
                ...client,
                proxies: [...proxies, parsed.tunnel],
              };
            }

            const nextProxies = [...proxies];
            nextProxies[existingIndex] = parsed.tunnel;
            return {
              ...client,
              proxies: nextProxies,
            };
          }),
        );
        queryClient.invalidateQueries({ queryKey: ['users', resourceScopeKey(resourceScope), 'client-tunnels'] });
        queryClient.invalidateQueries({ queryKey: ['users', resourceScopeKey(resourceScope), 'client-traffic'] });
        void resyncConsoleSnapshotSafely(queryClient, resourceScope, snapshotState, setStatus);
      }
      return;
    default:
      return;
  }
}

function parseSSE(buffer: string, onEvent: (eventType: string, data: string) => void): string {
  let remaining = buffer;

  while (true) {
    const delimiterIndex = remaining.indexOf('\n\n');
    if (delimiterIndex === -1) {
      break;
    }

    const rawEvent = remaining.slice(0, delimiterIndex);
    remaining = remaining.slice(delimiterIndex + 2);

    let eventType = 'message';
    const dataLines: string[] = [];

    for (const line of rawEvent.split('\n')) {
      if (line.startsWith('event:')) {
        eventType = line.slice(6).trim();
      }
      if (line.startsWith('data:')) {
        dataLines.push(line.slice(5).trim());
      }
    }

    if (dataLines.length > 0) {
      onEvent(eventType, dataLines.join('\n'));
    }
  }

  return remaining;
}

export function resolveEventStreamScope(
  resourceScope: ResourceScope | null,
  isAdmin: boolean,
  pathname: string,
  selectedActivityUserId?: string,
): ActivityReadScope | null {
  if (!pathname.startsWith('/dashboard')) return null;
  if (resourceScope) return resourceScope;
  if (!isAdmin) return null;
  if (pathname.startsWith('/dashboard/activity') && selectedActivityUserId) {
    return { kind: 'admin-user', userId: selectedActivityUserId };
  }
  return { kind: 'admin-global' };
}

function eventStreamScopesEqual(left: ActivityReadScope, right: ActivityReadScope) {
  return left.kind === right.kind
    && (left.kind !== 'admin-user' || (right.kind === 'admin-user' && left.userId === right.userId));
}

export function resolveEventStreamScopes(
  resourceScope: ResourceScope | null,
  sidebarScope: ResourceScope | null,
  isAdmin: boolean,
  pathname: string,
  selectedActivityUserId?: string,
) {
  const primary = resolveEventStreamScope(resourceScope, isAdmin, pathname, selectedActivityUserId);
  const secondary = primary && sidebarScope && !eventStreamScopesEqual(primary, sidebarScope)
    ? sidebarScope
    : null;
  const global = isAdmin
    && pathname.startsWith('/dashboard/users/')
    && primary?.kind !== 'admin-global'
    ? { kind: 'admin-global' as const }
    : null;
  return { primary, secondary, global };
}

const ignoreConnectionStatus: (status: ConnectionStatus) => void = () => undefined;

function useScopedEventStream(
  queryClient: EventStreamQueryClient,
  eventScope: ActivityReadScope | null,
  shouldConnect: boolean,
  setStatus: (status: ConnectionStatus) => void,
) {
  useEffect(() => {
    if (!shouldConnect || !eventScope) {
      setStatus('disconnected');
      return;
    }

    const snapshotState = createEventStreamSnapshotState();
    const activityState = createActivityRecoveryState();
    let cancelled = false;
    let activeController: AbortController | null = null;
    let hasConnected = false;

    const connect = async () => {
      while (!cancelled) {
        activeController = new AbortController();

        try {
          const isReconnect = hasConnected;
          setStatus(hasConnected ? 'reconnecting' : 'connecting');

          const response = await fetch(scopedEventStreamPath(eventScope), {
            method: 'GET',
            headers: {
              Accept: 'text/event-stream',
            },
            credentials: 'same-origin',
            signal: activeController.signal,
          });

          if (response.status === 401) {
            clearClientSessionAndRedirect();
            setStatus('disconnected');
            return;
          }

          if (!response.ok || !response.body) {
            throw new Error(`event stream failed: ${response.status}`);
          }

          if (isReconnect && eventScope.kind !== 'admin-global') {
            await resyncConsoleSnapshotSafely(queryClient, eventScope, snapshotState, setStatus);
          }

          hasConnected = true;
          setStatus('connected');

          const reader = response.body.getReader();
          const decoder = new TextDecoder();
          let buffer = '';

          while (!cancelled) {
            const { done, value } = await reader.read();
            if (cancelled) {
              return;
            }
            if (done) {
              throw new Error('event stream closed');
            }

            buffer += decoder.decode(value, { stream: true });
            buffer = parseSSE(buffer, (eventType, data) => applyEventForDiagnostics(
              queryClient,
              setStatus,
              snapshotState,
              eventType,
              data,
              activityState,
              eventScope,
            ));
          }
        } catch (error) {
          if (cancelled) {
            return;
          }
          if (error instanceof DOMException && error.name === 'AbortError') {
            return;
          }

          setStatus(hasConnected ? 'reconnecting' : 'connecting');
          await new Promise((resolve) => window.setTimeout(resolve, 1000));
        }
      }
    };

    void connect();

    return () => {
      cancelled = true;
      activityState.cancelled = true;
      if (activityState.retryTimer) clearTimeout(activityState.retryTimer);
      activeController?.abort();
      setStatus('disconnected');
    };
  }, [queryClient, eventScope, setStatus, shouldConnect]);
}

export function useEventStream() {
  const queryClient = useQueryClient();
  const setStatus = useConnectionStore((state) => state.setStatus);
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated);
  const principal = useAuthStore((state) => state.user);
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  const selectedActivityUserId = useRouterState({
    select: (state) => {
      const value = state.location.search.user_id;
      return typeof value === 'string' && value.length > 0 ? value : undefined;
    },
  });
  const resourceScope = useDashboardResourceScope();
  const sidebarScope = useDashboardSidebarScope();
  const resourceScopeKind = resourceScope?.kind;
  const resourceScopeUserId = resourceScope?.kind === 'admin-user' ? resourceScope.userId : undefined;
  const sidebarScopeKind = sidebarScope?.kind;
  const sidebarScopeUserId = sidebarScope?.kind === 'admin-user' ? sidebarScope.userId : undefined;
  const eventScopes = useMemo(() => {
    const stableResourceScope = resourceScopeKind === 'self'
      ? SELF_RESOURCE_SCOPE
      : resourceScopeKind === 'admin-user' && resourceScopeUserId
        ? { kind: 'admin-user' as const, userId: resourceScopeUserId }
        : null;
    const stableSidebarScope = sidebarScopeKind === 'self'
      ? SELF_RESOURCE_SCOPE
      : sidebarScopeKind === 'admin-user' && sidebarScopeUserId
        ? { kind: 'admin-user' as const, userId: sidebarScopeUserId }
        : null;
    return resolveEventStreamScopes(
      stableResourceScope,
      stableSidebarScope,
      principal?.is_admin === true,
      pathname,
      selectedActivityUserId,
    );
  }, [
    pathname,
    principal?.is_admin,
    resourceScopeKind,
    resourceScopeUserId,
    selectedActivityUserId,
    sidebarScopeKind,
    sidebarScopeUserId,
  ]);

  useScopedEventStream(queryClient, eventScopes.primary, isAuthenticated, setStatus);
  useScopedEventStream(queryClient, eventScopes.secondary, isAuthenticated, ignoreConnectionStatus);
  useScopedEventStream(queryClient, eventScopes.global, isAuthenticated, ignoreConnectionStatus);
}
