import { describe, expect, test } from 'vitest';

import type { Client, ProxyConfig, TunnelCapabilities } from '@/types';

import { buildDashboardTabMetrics, formatDashboardTabCount } from './dashboard-tab-metrics';

const capabilities: TunnelCapabilities = {
  can_resume: false,
  can_stop: true,
  can_edit: true,
  can_delete: true,
  can_migrate: true,
};

function createTunnel(overrides: Partial<ProxyConfig> = {}): ProxyConfig {
  return {
    id: 'tunnel-1',
    name: 'demo-tunnel',
    type: 'tcp',
    local_ip: '127.0.0.1',
    local_port: 8080,
    remote_port: 9000,
    domain: '',
    client_id: 'client-a',
    ingress_bps: 0,
    egress_bps: 0,
    created_at: '2026-01-01T00:00:00Z',
    desired_state: 'running',
    runtime_state: 'active',
    capabilities,
    ...overrides,
  };
}

function createClient(overrides: Partial<Client> = {}): Client {
  return {
    id: 'client-a',
    ingress_bps: 0,
    egress_bps: 0,
    info: {
      hostname: 'host-a',
      os: 'linux',
      arch: 'amd64',
      ip: '10.0.0.1',
      version: '1.0.0',
    },
    stats: null,
    online: true,
    proxies: [],
    ...overrides,
  };
}

describe('dashboard tab metrics', () => {
  test('hides tab counts when there are no items', () => {
    const metrics = buildDashboardTabMetrics([]);

    expect(formatDashboardTabCount(metrics.clients)).toBeNull();
    expect(formatDashboardTabCount(metrics.tunnels)).toBeNull();
  });

  test('shows only the total when every client and tunnel is healthy', () => {
    const metrics = buildDashboardTabMetrics([
      createClient({ proxies: [createTunnel()] }),
      createClient({
        id: 'client-b',
        info: { hostname: 'host-b', os: 'linux', arch: 'arm64', ip: '10.0.0.2', version: '1.0.0' },
      }),
    ]);

    expect(formatDashboardTabCount(metrics.clients)).toBe('2');
    expect(formatDashboardTabCount(metrics.tunnels)).toBe('1');
  });

  test('shows healthy over total when clients or tunnels are unhealthy', () => {
    const metrics = buildDashboardTabMetrics([
      createClient({ online: false, proxies: [createTunnel()] }),
      createClient({
        id: 'client-b',
        info: { hostname: 'host-b', os: 'linux', arch: 'arm64', ip: '10.0.0.2', version: '1.0.0' },
      }),
    ]);

    expect(formatDashboardTabCount(metrics.clients)).toBe('1/2');
    expect(formatDashboardTabCount(metrics.tunnels)).toBe('0/1');
  });

  test('dedupes client-to-client tunnels before counting health', () => {
    const c2cTunnel = createTunnel({
      id: 'tunnel-c2c',
      topology: 'client_to_client',
      owner_client_id: 'client-a',
      ingress: {
        location: 'client',
        client_id: 'client-b',
        type: 'tcp_listen',
        config: { bind_ip: '127.0.0.1', port: 9001 },
      },
      target: {
        location: 'client',
        client_id: 'client-a',
        type: 'tcp_service',
        config: { ip: '127.0.0.1', port: 8080 },
      },
    });
    const metrics = buildDashboardTabMetrics([
      createClient({ proxies: [c2cTunnel] }),
      createClient({
        id: 'client-b',
        info: { hostname: 'host-b', os: 'linux', arch: 'arm64', ip: '10.0.0.2', version: '1.0.0' },
        proxies: [c2cTunnel],
      }),
    ]);

    expect(formatDashboardTabCount(metrics.tunnels)).toBe('1');
  });
});
