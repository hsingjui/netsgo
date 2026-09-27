import { describe, expect, test } from 'vitest';

import type { Client, ProxyConfig } from '@/types';

import { buildTunnelMigrationInput, getTunnelMigrationCandidates } from '@/lib/tunnel-migration';

function createClient(id: string): Client {
  return {
    id,
    ingress_bps: 0,
    egress_bps: 0,
    info: {
      hostname: id,
      os: 'linux',
      arch: 'amd64',
      ip: '127.0.0.1',
      version: 'dev',
    },
    stats: null,
    proxies: [],
    online: true,
  };
}

function createTunnel(overrides: Partial<ProxyConfig> = {}): ProxyConfig {
  return {
    id: 'tunnel-1',
    name: 'demo',
    revision: 4,
    topology: 'server_expose',
    owner_client_id: 'target-current',
    target: {
      location: 'client',
      client_id: 'target-current',
      type: 'tcp_service',
      config: { ip: '127.0.0.1', port: 22 },
    },
    ingress: {
      location: 'server',
      type: 'tcp_listen',
      config: { bind_ip: '0.0.0.0', port: 18080, allowed_source_cidrs: ['0.0.0.0/0', '::/0'] },
    },
    type: 'tcp',
    local_ip: '127.0.0.1',
    local_port: 22,
    remote_port: 18080,
    domain: '',
    client_id: 'target-current',
    ingress_bps: 0,
    egress_bps: 0,
    created_at: '2026-07-10T00:00:00Z',
    desired_state: 'running',
    runtime_state: 'offline',
    capabilities: {
      can_resume: false,
      can_stop: true,
      can_edit: true,
      can_delete: true,
      can_migrate: true,
    },
    ...overrides,
  };
}

describe('getTunnelMigrationCandidates', () => {
  test('excludes the current target client', () => {
    const candidates = getTunnelMigrationCandidates(createTunnel(), [
      createClient('target-current'),
      createClient('target-next'),
    ]);

    expect(candidates.map((client) => client.id)).toEqual(['target-next']);
  });

  test('keeps an offline registered client as a migration candidate', () => {
    const offlineCandidate = { ...createClient('target-next'), online: false };

    expect(getTunnelMigrationCandidates(createTunnel(), [offlineCandidate])).toEqual([offlineCandidate]);
  });

  test('also excludes the unchanged client-to-client ingress', () => {
    const tunnel = createTunnel({
      topology: 'client_to_client',
      ingress: {
        location: 'client',
        client_id: 'ingress-client',
        type: 'tcp_listen',
        config: { bind_ip: '127.0.0.1', port: 18080, allowed_source_cidrs: ['127.0.0.0/8'] },
      },
    });
    const candidates = getTunnelMigrationCandidates(tunnel, [
      createClient('target-current'),
      createClient('ingress-client'),
      createClient('target-next'),
    ]);

    expect(candidates.map((client) => client.id)).toEqual(['target-next']);
  });
});

describe('buildTunnelMigrationInput', () => {
  test('submits the selected candidate with the current tunnel revision', () => {
    const clients = [createClient('target-current'), createClient('target-next')];

    expect(buildTunnelMigrationInput(createTunnel({ id: 'stable-id', revision: 12 }), 'target-next', clients)).toEqual({
      tunnelId: 'stable-id',
      expected_revision: 12,
      target_client_id: 'target-next',
    });
  });

  test('rejects missing or invalid revision and unavailable target selection', () => {
    const clients = [createClient('target-current'), createClient('target-next')];

    expect(buildTunnelMigrationInput(createTunnel({ revision: undefined }), 'target-next', clients)).toBeNull();
    expect(buildTunnelMigrationInput(createTunnel({ revision: 1.5 }), 'target-next', clients)).toBeNull();
    expect(buildTunnelMigrationInput(createTunnel(), '', clients)).toBeNull();
    expect(buildTunnelMigrationInput(createTunnel(), 'target-current', clients)).toBeNull();
    expect(buildTunnelMigrationInput(createTunnel(), 'missing-client', clients)).toBeNull();
    expect(buildTunnelMigrationInput(createTunnel(), 'target-next', [createClient('target-current')])).toBeNull();
  });

  test('rejects the unchanged client-to-client ingress as a migration target', () => {
    const tunnel = createTunnel({
      topology: 'client_to_client',
      ingress: {
        location: 'client',
        client_id: 'ingress-client',
        type: 'tcp_listen',
        config: { bind_ip: '127.0.0.1', port: 18080, allowed_source_cidrs: ['127.0.0.0/8'] },
      },
    });
    const clients = [createClient('target-current'), createClient('ingress-client'), createClient('target-next')];

    expect(buildTunnelMigrationInput(tunnel, 'ingress-client', clients)).toBeNull();
  });
});
