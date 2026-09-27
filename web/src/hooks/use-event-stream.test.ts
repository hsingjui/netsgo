import { describe, expect, test } from 'vitest';
import { QueryClient } from '@tanstack/react-query';

import type { ActivityItem, Client, ConsoleSummary, ProxyConfig, ResourceBootstrap } from '@/types';
import { SELF_RESOURCE_SCOPE, scopedQueryKey } from '@/lib/resource-scope';
import { buildActivityQueryKey } from './use-activity';

import {
  applyEventForDiagnostics,
  createActivityRecoveryState,
  createEventStreamSnapshotState,
  resolveEventStreamScope,
  resolveEventStreamScopes,
} from './use-event-stream';
import { webhookDeliveriesQueryKey, webhookDeliveryQueryKey, webhooksQueryKey } from './use-webhooks';

const selfScope = SELF_RESOURCE_SCOPE;
const clientsKey = scopedQueryKey(selfScope, 'clients');
const consoleSummaryKey = scopedQueryKey(selfScope, 'console-summary');
const resourceBootstrapKey = scopedQueryKey(selfScope, 'resource-bootstrap');
const clientTunnelKey = (clientId: string, role: string) => scopedQueryKey(selfScope, 'client-tunnels', clientId, role);
const clientTrafficKey = (clientId: string, range: string, tunnel = '') => scopedQueryKey(selfScope, 'client-traffic', clientId, range, tunnel);

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (reason?: unknown) => void;
}

function createDeferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function createTunnel(
  runtimeState: ProxyConfig['runtime_state'],
  overrides: Partial<ProxyConfig> = {},
): ProxyConfig {
  return {
    id: 'tunnel-1',
    name: 'demo',
    type: 'tcp',
    local_ip: '127.0.0.1',
    local_port: 3000,
    remote_port: 18080,
    domain: '',
    client_id: 'client-1',
    ingress_bps: 0,
    egress_bps: 0,
    created_at: '2026-05-08T01:00:00Z',
    desired_state: 'running',
    runtime_state: runtimeState,
    capabilities: {
      can_resume: false,
      can_stop: runtimeState === 'exposed',
      can_edit: false,
      can_delete: runtimeState !== 'pending',
      can_migrate: runtimeState !== 'pending',
    },
    ...overrides,
  };
}

function createClientWithTunnels(id: string, proxies: ProxyConfig[]): Client {
  return {
    id,
    ingress_bps: 0,
    egress_bps: 0,
    info: {
      hostname: id,
      os: 'linux',
      arch: 'amd64',
      ip: '127.0.0.1',
      version: 'v0.1.0',
    },
    stats: null,
    proxies,
    online: true,
  };
}

function createClient(runtimeState: ProxyConfig['runtime_state']): Client {
  return createClientWithTunnels('client-1', [createTunnel(runtimeState)]);
}

function tunnelChangedPayload(clientId: string, action: string, tunnel: ProxyConfig) {
  return JSON.stringify({
    client_id: clientId,
    action,
    tunnel,
  });
}

function tunnelChangedEvent(runtimeState: ProxyConfig['runtime_state'], action: string) {
  return tunnelChangedPayload('client-1', action, createTunnel(runtimeState));
}

function summary(activeTunnels = 0): ConsoleSummary {
  return {
    total_clients: 1,
    online_clients: 1,
    offline_clients: 0,
    total_tunnels: activeTunnels,
    active_tunnels: activeTunnels,
    inactive_tunnels: 0,
    pending_tunnels: 0,
    offline_tunnels: 0,
    stopped_tunnels: 0,
    error_tunnels: 0,
  };
}

function bootstrap(version = 'v0.1.0'): ResourceBootstrap {
  return {
    version,
    server_addr: 'https://netsgo.example.com',
    allowed_ports: [{ start: 10000, end: 20000 }],
  };
}

function snapshotPayload(runtimeState: ProxyConfig['runtime_state'], generatedAt?: string) {
  return JSON.stringify({
    clients: [createClient(runtimeState)],
    summary: summary(1),
    bootstrap: bootstrap(),
    generated_at: generatedAt ?? '2026-05-08T01:00:00Z',
    fresh_until: '2026-05-08T01:00:15Z',
  });
}

function snapshotResponse(runtimeState: ProxyConfig['runtime_state']) {
  return new Response(snapshotPayload(runtimeState), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });
}

function clientsSnapshotResponse(
  clients: Client[],
  overrides: Record<string, unknown> = {},
) {
  return new Response(JSON.stringify({
    clients,
    summary: summary(),
    bootstrap: bootstrap(),
    generated_at: '2026-05-08T01:00:00Z',
    fresh_until: '2026-05-08T01:00:15Z',
    ...overrides,
  }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });
}

async function waitForRequests(requests: unknown[], count: number) {
  const deadline = Date.now() + 500;
  while (requests.length < count && Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  expect(requests.length).toBe(count);
}

async function flushAsyncWork() {
  await Promise.resolve();
  await new Promise((resolve) => setTimeout(resolve, 0));
}

function activity(id: number): ActivityItem {
  return {
    id, occurred_at: '2026-07-23T00:00:00Z', recorded_at: '2026-07-23T00:00:00Z',
    severity: 'info', category: 'client', action: 'online', source: 'server',
    actor: { type: 'system' }, payload_version: 1, payload: {},
    clients: [{ client_id: 'client-1', relation: 'subject' }], tunnels: [],
  };
}

describe('event stream scope selection', () => {
  test('uses the administrator-global stream outside resource workspaces', () => {
    expect(resolveEventStreamScope(null, true, '/dashboard/users')).toEqual({ kind: 'admin-global' });
    expect(resolveEventStreamScope(null, true, '/dashboard/admin/security')).toEqual({ kind: 'admin-global' });
  });

  test('keeps target-user resource and filtered activity streams explicitly scoped', () => {
    expect(resolveEventStreamScope(
      { kind: 'admin-user', userId: 'user-a' },
      true,
      '/dashboard/users/user-a',
    )).toEqual({ kind: 'admin-user', userId: 'user-a' });
    expect(resolveEventStreamScope(null, true, '/dashboard/activity', 'user-b')).toEqual({
      kind: 'admin-user',
      userId: 'user-b',
    });
  });

  test('does not connect a global stream before entering the authenticated dashboard', () => {
    expect(resolveEventStreamScope(null, true, '/login')).toBeNull();
    expect(resolveEventStreamScope(null, false, '/dashboard/users')).toBeNull();
  });

  test('keeps a second self stream for an administrator sidebar on global pages', () => {
    expect(resolveEventStreamScopes(
      null,
      SELF_RESOURCE_SCOPE,
      true,
      '/dashboard/users',
    )).toEqual({
      primary: { kind: 'admin-global' },
      secondary: SELF_RESOURCE_SCOPE,
      global: null,
    });
  });

  test('keeps the self sidebar live while viewing another user activity scope', () => {
    expect(resolveEventStreamScopes(
      null,
      SELF_RESOURCE_SCOPE,
      true,
      '/dashboard/activity',
      'user-b',
    )).toEqual({
      primary: { kind: 'admin-user', userId: 'user-b' },
      secondary: SELF_RESOURCE_SCOPE,
      global: null,
    });
  });

  test('adds the global list stream without duplicating the matching target-user stream', () => {
    const targetScope = { kind: 'admin-user' as const, userId: 'user-a' };
    expect(resolveEventStreamScopes(
      targetScope,
      targetScope,
      true,
      '/dashboard/users/user-a',
    )).toEqual({
      primary: targetScope,
      secondary: null,
      global: { kind: 'admin-global' },
    });
  });
});

describe('use-event-stream diagnostics', () => {
  test('invalidates every administrator user-list page on a global list-change hint', () => {
    const queryClient = new QueryClient();
    const listKey = ['admin-users', 50, null, '', null, null] as const;
    const detailKey = ['admin-user', 'user-a'] as const;
    const otherDetailKey = ['admin-user', 'user-b'] as const;
    queryClient.setQueryData(listKey, { items: [] });
    queryClient.setQueryData(detailKey, { id: 'user-a' });
    queryClient.setQueryData(otherDetailKey, { id: 'user-b' });

    applyEventForDiagnostics(
      queryClient,
      () => undefined,
      createEventStreamSnapshotState(),
      'user_list_changed',
      JSON.stringify({ action: 'deleted', user_id: 'user-a' }),
      undefined,
      { kind: 'admin-global' },
    );

    expect(queryClient.getQueryState(listKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(detailKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(otherDetailKey)?.isInvalidated).toBe(false);
    queryClient.clear();
  });

  test('invalidates all administrator user details when a global list-change hint is malformed', () => {
    const queryClient = new QueryClient();
    const detailKey = ['admin-user', 'user-a'] as const;
    queryClient.setQueryData(detailKey, { id: 'user-a' });

    applyEventForDiagnostics(
      queryClient,
      () => undefined,
      createEventStreamSnapshotState(),
      'user_list_changed',
      '{}',
      undefined,
      { kind: 'admin-global' },
    );

    expect(queryClient.getQueryState(detailKey)?.isInvalidated).toBe(true);
    queryClient.clear();
  });

  test('ignores a global user-list hint on a user-scoped stream', () => {
    const queryClient = new QueryClient();
    const listKey = ['admin-users', 50, null, '', null, null] as const;
    queryClient.setQueryData(listKey, { items: [] });

    applyEventForDiagnostics(
      queryClient,
      () => undefined,
      createEventStreamSnapshotState(),
      'user_list_changed',
      '{}',
    );

    expect(queryClient.getQueryState(listKey)?.isInvalidated).toBe(false);
    queryClient.clear();
  });

  test('keeps newer tunnel_changed state when an older console snapshot resolves later', async () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData<Client[]>(clientsKey, [createClient('pending')]);

    const originalFetch = globalThis.fetch;
    const requests: Deferred<Response>[] = [];
    globalThis.fetch = (() => {
      const deferred = createDeferred<Response>();
      requests.push(deferred);
      return deferred.promise;
    }) as typeof fetch;

    try {
      const statuses: string[] = [];
      const snapshotState = createEventStreamSnapshotState();
      applyEventForDiagnostics(queryClient, (status) => statuses.push(status), snapshotState, 'tunnel_changed', tunnelChangedEvent('pending', 'pending'));
      await waitForRequests(requests, 1);

      applyEventForDiagnostics(queryClient, (status) => statuses.push(status), snapshotState, 'tunnel_changed', tunnelChangedEvent('exposed', 'restored'));
      await waitForRequests(requests, 2);

      expect(queryClient.getQueryData<Client[]>(clientsKey)?.[0]?.proxies?.[0]?.runtime_state).toBe('exposed');

      requests[1].resolve(snapshotResponse('exposed'));
      await flushAsyncWork();
      expect(queryClient.getQueryData<Client[]>(clientsKey)?.[0]?.proxies?.[0]?.runtime_state).toBe('exposed');

      requests[0].resolve(snapshotResponse('pending'));
      await flushAsyncWork();

      expect(queryClient.getQueryData<Client[]>(clientsKey)?.[0]?.proxies?.[0]?.runtime_state).toBe('exposed');
      expect(statuses).toEqual(['connected']);
    } finally {
      globalThis.fetch = originalFetch;
      queryClient.clear();
    }
  });

  test('ignores older SSE snapshots after a newer snapshot has been applied', () => {
    const queryClient = new QueryClient();
    const snapshotState = createEventStreamSnapshotState();

    try {
      applyEventForDiagnostics(
        queryClient,
        () => undefined,
        snapshotState,
        'snapshot',
        snapshotPayload('exposed', '2026-05-08T01:00:02Z'),
      );
      expect(queryClient.getQueryData<Client[]>(clientsKey)?.[0]?.proxies?.[0]?.runtime_state).toBe('exposed');
      expect(queryClient.getQueryData(resourceBootstrapKey)).toEqual(bootstrap());

      applyEventForDiagnostics(
        queryClient,
        () => undefined,
        snapshotState,
        'snapshot',
        snapshotPayload('pending', '2026-05-08T01:00:01Z'),
      );

      expect(queryClient.getQueryData<Client[]>(clientsKey)?.[0]?.proxies?.[0]?.runtime_state).toBe('exposed');
    } finally {
      queryClient.clear();
    }
  });

  test('rejects a scoped snapshot that omits the resource bootstrap contract', () => {
    const queryClient = new QueryClient();
    const snapshotState = createEventStreamSnapshotState();
    queryClient.setQueryData<Client[]>(clientsKey, [createClient('exposed')]);

    applyEventForDiagnostics(
      queryClient,
      () => undefined,
      snapshotState,
      'snapshot',
      JSON.stringify({
        clients: [createClient('pending')],
        summary: summary(1),
        generated_at: '2026-05-08T01:00:01Z',
        fresh_until: '2026-05-08T01:00:16Z',
      }),
    );

    expect(queryClient.getQueryData<Client[]>(clientsKey)?.[0]?.proxies?.[0]?.runtime_state).toBe('exposed');
    expect(queryClient.getQueryData(resourceBootstrapKey)).toBeUndefined();
    queryClient.clear();
  });

  test('accepts an administrator-global handshake without requiring a snapshot', () => {
    const queryClient = new QueryClient();
    const snapshotState = createEventStreamSnapshotState();
    const activityState = createActivityRecoveryState();

    applyEventForDiagnostics(
      queryClient,
      () => undefined,
      snapshotState,
      'ready',
      JSON.stringify({ activity_cursor: 12 }),
      activityState,
      { kind: 'admin-global' },
    );

    expect(activityState.lastScannedId).toBe(12);
    expect(queryClient.getQueryData(clientsKey)).toBeUndefined();
    expect(queryClient.getQueryData(resourceBootstrapKey)).toBeUndefined();
    activityState.cancelled = true;
    queryClient.clear();
  });

  test('moves a server-expose tunnel from the old owner to the new owner', async () => {
    const queryClient = new QueryClient();
    const oldOwnerId = 'old-owner';
    const newOwnerId = 'new-owner';
    const oldTunnel = createTunnel('exposed', {
      revision: 7,
      topology: 'server_expose',
      client_id: oldOwnerId,
      owner_client_id: oldOwnerId,
      ingress: {
        location: 'server',
        type: 'tcp_listen',
        config: { bind_ip: '0.0.0.0', port: 18080 },
      },
      target: {
        location: 'client',
        client_id: oldOwnerId,
        type: 'tcp_service',
        config: { host: '127.0.0.1', port: 3000 },
      },
    });
    const migratedTunnel: ProxyConfig = {
      ...oldTunnel,
      revision: 8,
      client_id: newOwnerId,
      owner_client_id: newOwnerId,
      runtime_state: 'pending',
      target: {
        location: 'client',
        client_id: newOwnerId,
        type: 'tcp_service',
        config: { host: '127.0.0.1', port: 3000 },
      },
      capabilities: {
        ...oldTunnel.capabilities,
        can_stop: false,
        can_migrate: false,
      },
    };
    const finalClients = [
      createClientWithTunnels(oldOwnerId, []),
      createClientWithTunnels(newOwnerId, [migratedTunnel]),
    ];
    queryClient.setQueryData<Client[]>(clientsKey, [
      createClientWithTunnels(oldOwnerId, [oldTunnel]),
      createClientWithTunnels(newOwnerId, []),
    ]);

    const originalFetch = globalThis.fetch;
    const requests: Deferred<Response>[] = [];
    globalThis.fetch = (() => {
      const deferred = createDeferred<Response>();
      requests.push(deferred);
      return deferred.promise;
    }) as typeof fetch;

    try {
      const snapshotState = createEventStreamSnapshotState();
      applyEventForDiagnostics(
        queryClient,
        () => undefined,
        snapshotState,
        'tunnel_changed',
        tunnelChangedPayload(oldOwnerId, 'migrated_out', oldTunnel),
      );

      let clients = queryClient.getQueryData<Client[]>(clientsKey);
      expect(clients?.find((client) => client.id === oldOwnerId)?.proxies).toEqual([]);
      expect(clients?.find((client) => client.id === newOwnerId)?.proxies).toEqual([]);

      applyEventForDiagnostics(
        queryClient,
        () => undefined,
        snapshotState,
        'tunnel_changed',
        tunnelChangedPayload(newOwnerId, 'migrated_in', migratedTunnel),
      );

      clients = queryClient.getQueryData<Client[]>(clientsKey);
      expect(clients?.find((client) => client.id === oldOwnerId)?.proxies).toEqual([]);
      expect(clients?.find((client) => client.id === newOwnerId)?.proxies).toEqual([migratedTunnel]);

      await waitForRequests(requests, 2);
      requests[0].resolve(clientsSnapshotResponse(finalClients));
      requests[1].resolve(clientsSnapshotResponse(finalClients));
      await flushAsyncWork();
    } finally {
      globalThis.fetch = originalFetch;
      queryClient.clear();
    }
  });

  test('keeps the c2c ingress copy on migrated_out and updates it on migrated_in', async () => {
    const queryClient = new QueryClient();
    const ingressId = 'ingress-client';
    const oldOwnerId = 'old-target';
    const newOwnerId = 'new-target';
    const oldTunnel = createTunnel('active', {
      revision: 11,
      topology: 'client_to_client',
      client_id: oldOwnerId,
      owner_client_id: oldOwnerId,
      ingress: {
        location: 'client',
        client_id: ingressId,
        type: 'tcp_listen',
        config: { bind_ip: '0.0.0.0', port: 18080 },
      },
      target: {
        location: 'client',
        client_id: oldOwnerId,
        type: 'tcp_service',
        config: { host: '127.0.0.1', port: 3000 },
      },
    });
    const migratedTunnel: ProxyConfig = {
      ...oldTunnel,
      revision: 12,
      client_id: newOwnerId,
      owner_client_id: newOwnerId,
      runtime_state: 'pending',
      target: {
        location: 'client',
        client_id: newOwnerId,
        type: 'tcp_service',
        config: { host: '127.0.0.1', port: 3000 },
      },
      capabilities: {
        ...oldTunnel.capabilities,
        can_stop: false,
        can_migrate: false,
      },
    };
    const finalClients = [
      createClientWithTunnels(ingressId, [migratedTunnel]),
      createClientWithTunnels(oldOwnerId, []),
      createClientWithTunnels(newOwnerId, [migratedTunnel]),
    ];
    queryClient.setQueryData<Client[]>(clientsKey, [
      createClientWithTunnels(ingressId, [oldTunnel]),
      createClientWithTunnels(oldOwnerId, [oldTunnel]),
      createClientWithTunnels(newOwnerId, []),
    ]);
    queryClient.setQueryData(clientTunnelKey(oldOwnerId, 'owner'), [oldTunnel]);
    queryClient.setQueryData(clientTunnelKey(newOwnerId, 'owner'), []);
    queryClient.setQueryData(clientTunnelKey(ingressId, 'ingress'), [oldTunnel]);
    queryClient.setQueryData(clientTrafficKey(oldOwnerId, '60s'), { resolution: 'second', items: [] });
    queryClient.setQueryData(clientTrafficKey(newOwnerId, '60s', 'demo'), { resolution: 'second', items: [] });
    queryClient.setQueryData(consoleSummaryKey, { marker: 'stale-summary' });
    queryClient.setQueryData(resourceBootstrapKey, bootstrap('stale-version'));

    const originalFetch = globalThis.fetch;
    const requests: Deferred<Response>[] = [];
    globalThis.fetch = (() => {
      const deferred = createDeferred<Response>();
      requests.push(deferred);
      return deferred.promise;
    }) as typeof fetch;

    try {
      const snapshotState = createEventStreamSnapshotState();
      applyEventForDiagnostics(
        queryClient,
        () => undefined,
        snapshotState,
        'tunnel_changed',
        tunnelChangedPayload(oldOwnerId, 'migrated_out', oldTunnel),
      );

      let clients = queryClient.getQueryData<Client[]>(clientsKey);
      expect(clients?.find((client) => client.id === oldOwnerId)?.proxies).toEqual([]);
      expect(clients?.find((client) => client.id === ingressId)?.proxies).toEqual([oldTunnel]);

      applyEventForDiagnostics(
        queryClient,
        () => undefined,
        snapshotState,
        'tunnel_changed',
        tunnelChangedPayload(newOwnerId, 'migrated_in', migratedTunnel),
      );

      clients = queryClient.getQueryData<Client[]>(clientsKey);
      expect(clients?.find((client) => client.id === oldOwnerId)?.proxies).toEqual([]);
      expect(clients?.find((client) => client.id === ingressId)?.proxies).toEqual([migratedTunnel]);
      expect(clients?.find((client) => client.id === newOwnerId)?.proxies).toEqual([migratedTunnel]);
      expect(queryClient.getQueryState(clientTunnelKey(oldOwnerId, 'owner'))?.isInvalidated).toBe(true);
      expect(queryClient.getQueryState(clientTunnelKey(newOwnerId, 'owner'))?.isInvalidated).toBe(true);
      expect(queryClient.getQueryState(clientTunnelKey(ingressId, 'ingress'))?.isInvalidated).toBe(true);
      expect(queryClient.getQueryState(clientTrafficKey(oldOwnerId, '60s'))?.isInvalidated).toBe(true);
      expect(queryClient.getQueryState(clientTrafficKey(newOwnerId, '60s', 'demo'))?.isInvalidated).toBe(true);
      expect(queryClient.getQueryData(consoleSummaryKey)).toEqual({ marker: 'stale-summary' });
      expect(queryClient.getQueryData(resourceBootstrapKey)).toEqual(bootstrap('stale-version'));
      expect(queryClient.getQueryState(consoleSummaryKey)?.isInvalidated).toBe(false);
      expect(queryClient.getQueryState(resourceBootstrapKey)?.isInvalidated).toBe(false);

      await waitForRequests(requests, 2);
      requests[0].resolve(clientsSnapshotResponse(finalClients));
      requests[1].resolve(clientsSnapshotResponse(finalClients));
      await flushAsyncWork();
    } finally {
      globalThis.fetch = originalFetch;
      queryClient.clear();
    }
  });

  test('ignores a stale migrated_out resync failure after migrated_in resync succeeds', async () => {
    const queryClient = new QueryClient();
    const oldOwnerId = 'old-owner';
    const newOwnerId = 'new-owner';
    const oldTunnel = createTunnel('exposed', {
      client_id: oldOwnerId,
      owner_client_id: oldOwnerId,
    });
    const migratedTunnel = createTunnel('pending', {
      ...oldTunnel,
      client_id: newOwnerId,
      owner_client_id: newOwnerId,
    });
    const finalClients = [
      createClientWithTunnels(oldOwnerId, []),
      createClientWithTunnels(newOwnerId, [migratedTunnel]),
    ];
    queryClient.setQueryData<Client[]>(clientsKey, [
      createClientWithTunnels(oldOwnerId, [oldTunnel]),
      createClientWithTunnels(newOwnerId, []),
    ]);
    queryClient.setQueryData(consoleSummaryKey, summary(1));
    queryClient.setQueryData(resourceBootstrapKey, bootstrap('v0.1.0'));

    const originalFetch = globalThis.fetch;
    const requests: Deferred<Response>[] = [];
    globalThis.fetch = (() => {
      const deferred = createDeferred<Response>();
      requests.push(deferred);
      return deferred.promise;
    }) as typeof fetch;

    try {
      const statuses: string[] = [];
      const snapshotState = createEventStreamSnapshotState();
      applyEventForDiagnostics(
        queryClient,
        (status) => statuses.push(status),
        snapshotState,
        'tunnel_changed',
        tunnelChangedPayload(oldOwnerId, 'migrated_out', oldTunnel),
      );
      applyEventForDiagnostics(
        queryClient,
        (status) => statuses.push(status),
        snapshotState,
        'tunnel_changed',
        tunnelChangedPayload(newOwnerId, 'migrated_in', migratedTunnel),
      );

      requests[1].resolve(clientsSnapshotResponse(finalClients, {
        summary: summary(1),
        bootstrap: bootstrap('v0.2.0'),
        generated_at: '2026-05-08T01:00:02Z',
      }));
      await flushAsyncWork();
      requests[0].reject(new Error('stale migrated_out resync failed'));
      await flushAsyncWork();

      expect(queryClient.getQueryData<Client[]>(clientsKey)).toEqual(finalClients);
      expect(queryClient.getQueryData(consoleSummaryKey)).toEqual(summary(1));
      expect(queryClient.getQueryData(resourceBootstrapKey)).toEqual(bootstrap('v0.2.0'));
      expect(queryClient.getQueryState(clientsKey)?.isInvalidated).toBe(false);
      expect(queryClient.getQueryState(consoleSummaryKey)?.isInvalidated).toBe(false);
      expect(queryClient.getQueryState(resourceBootstrapKey)?.isInvalidated).toBe(false);
      expect(statuses).toEqual(['connected']);
    } finally {
      globalThis.fetch = originalFetch;
      queryClient.clear();
    }
  });
  test('recovers activity gaps from the durable global cursor', async () => {
    const queryClient = new QueryClient();
    const snapshotState = createEventStreamSnapshotState();
    const activityState = createActivityRecoveryState();
    const activityKey = buildActivityQueryKey(selfScope, {
      scope: 'global',
      limit: 50,
      severities: ['error', 'info', 'warning'],
      categories: [],
    });
    queryClient.setQueryData(activityKey, { pages: [{ items: [], has_more: false, direction: 'before' }], pageParams: [undefined] });
    const originalFetch = globalThis.fetch;
    const calls: string[] = [];
    globalThis.fetch = (async (input) => {
      calls.push(String(input));
      return new Response(JSON.stringify({ items: [activity(12), activity(11)], next_cursor: 12, has_more: false, direction: 'after' }), {
        status: 200, headers: { 'Content-Type': 'application/json' },
      });
    }) as typeof fetch;
    try {
      applyEventForDiagnostics(queryClient, () => undefined, snapshotState, 'ready', JSON.stringify({ activity_cursor: 10 }), activityState);
      applyEventForDiagnostics(queryClient, () => undefined, snapshotState, 'activity_event', JSON.stringify(activity(12)), activityState);
      await flushAsyncWork();
      await flushAsyncWork();
      expect(calls).toHaveLength(1);
      expect(calls[0]).toContain('after=10');
      expect(activityState.lastScannedId).toBe(12);
      const cached = queryClient.getQueryData<{ pages: { items: ActivityItem[] }[] }>(activityKey);
      expect(cached?.pages[0].items.map((entry) => entry.id)).toEqual([12, 11]);
    } finally {
      globalThis.fetch = originalFetch;
      activityState.cancelled = true;
      queryClient.clear();
    }
  });

  test('invalid activity payload invalidates cache without advancing cursor', () => {
    const queryClient = new QueryClient();
    const snapshotState = createEventStreamSnapshotState();
    const activityState = createActivityRecoveryState();
    applyEventForDiagnostics(queryClient, () => undefined, snapshotState, 'ready', JSON.stringify({ activity_cursor: 7 }), activityState);
    applyEventForDiagnostics(queryClient, () => undefined, snapshotState, 'activity_event', '{"id":"bad"}', activityState);
    expect(activityState.lastScannedId).toBe(7);
    expect(activityState.targetId).toBe(7);
    activityState.cancelled = true;
    queryClient.clear();
  });

});

describe('use-event-stream webhook events', () => {
  const unrelatedKey = scopedQueryKey(selfScope, 'clients');

  test('invalidates the webhook list that useWebhooks subscribes to on webhook_changed', () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData(webhooksQueryKey, []);
    queryClient.setQueryData(unrelatedKey, []);

    applyEventForDiagnostics(
      queryClient,
      () => undefined,
      createEventStreamSnapshotState(),
      'webhook_changed',
      JSON.stringify({ webhook_id: 'wh_1' }),
    );

    expect(queryClient.getQueryState(webhooksQueryKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(unrelatedKey)?.isInvalidated).toBe(false);
    queryClient.clear();
  });

  test('invalidates every delivery-list filter, the delivery detail, and the webhook list on webhook_delivery_changed', () => {
    const queryClient = new QueryClient();
    const listKey = webhookDeliveriesQueryKey('wh_1', 'all');
    const failedListKey = webhookDeliveriesQueryKey('wh_1', 'failed');
    const detailKey = webhookDeliveryQueryKey('dlv_1');
    const otherListKey = webhookDeliveriesQueryKey('wh_2', 'all');
    queryClient.setQueryData(listKey, { items: [] });
    queryClient.setQueryData(failedListKey, { items: [] });
    queryClient.setQueryData(detailKey, { id: 'dlv_1' });
    queryClient.setQueryData(otherListKey, { items: [] });
    queryClient.setQueryData(webhooksQueryKey, []);

    applyEventForDiagnostics(
      queryClient,
      () => undefined,
      createEventStreamSnapshotState(),
      'webhook_delivery_changed',
      JSON.stringify({ webhook_id: 'wh_1', delivery_id: 'dlv_1', status: 'success' }),
    );

    expect(queryClient.getQueryState(listKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(failedListKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(detailKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(webhooksQueryKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(otherListKey)?.isInvalidated).toBe(false);
    queryClient.clear();
  });

  test('ignores malformed webhook event payloads', () => {
    const queryClient = new QueryClient();
    const listKey = webhookDeliveriesQueryKey('wh_1', 'all');
    const detailKey = webhookDeliveryQueryKey('dlv_1');
    queryClient.setQueryData(webhooksQueryKey, []);
    queryClient.setQueryData(listKey, { items: [] });
    queryClient.setQueryData(detailKey, { id: 'dlv_1' });

    applyEventForDiagnostics(
      queryClient,
      () => undefined,
      createEventStreamSnapshotState(),
      'webhook_changed',
      JSON.stringify({ foo: 1 }),
    );
    applyEventForDiagnostics(
      queryClient,
      () => undefined,
      createEventStreamSnapshotState(),
      'webhook_delivery_changed',
      JSON.stringify({ webhook_id: 'wh_1' }),
    );
    applyEventForDiagnostics(
      queryClient,
      () => undefined,
      createEventStreamSnapshotState(),
      'webhook_delivery_changed',
      JSON.stringify({ delivery_id: 'dlv_1' }),
    );
    applyEventForDiagnostics(
      queryClient,
      () => undefined,
      createEventStreamSnapshotState(),
      'webhook_delivery_changed',
      JSON.stringify({ webhook_id: 'wh_1', delivery_id: 'dlv_1' }),
    );

    expect(queryClient.getQueryState(webhooksQueryKey)?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(listKey)?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(detailKey)?.isInvalidated).toBe(false);
    queryClient.clear();
  });

  test('leaves unrelated queries untouched by webhook events', () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData(unrelatedKey, []);

    applyEventForDiagnostics(
      queryClient,
      () => undefined,
      createEventStreamSnapshotState(),
      'webhook_changed',
      JSON.stringify({ webhook_id: 'wh_1' }),
    );
    applyEventForDiagnostics(
      queryClient,
      () => undefined,
      createEventStreamSnapshotState(),
      'webhook_delivery_changed',
      JSON.stringify({ webhook_id: 'wh_1', delivery_id: 'dlv_1', status: 'failed' }),
    );

    expect(queryClient.getQueryState(unrelatedKey)?.isInvalidated).toBe(false);
    queryClient.clear();
  });
});
