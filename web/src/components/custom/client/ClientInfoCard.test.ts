import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';

import type { Client, SystemStats } from '@/types';

import { ClientInfoCard } from './ClientInfoCard';

function createStats(overrides: Partial<SystemStats> = {}): SystemStats {
  return {
    cpu_usage: 23,
    mem_total: 1024,
    mem_used: 512,
    mem_usage: 50,
    disk_total: 2048,
    disk_used: 1024,
    disk_usage: 50,
    disk_partitions: [],
    net_sent: 100,
    net_recv: 200,
    net_sent_speed: 10,
    net_recv_speed: 20,
    uptime: 3600,
    process_uptime: 120,
    num_cpu: 4,
    app_mem_used: 128,
    app_mem_sys: 256,
    ...overrides,
  };
}

function createClient(overrides: Partial<Client> = {}): Client {
  return {
    id: 'client-1',
    ingress_bps: 0,
    egress_bps: 0,
    info: {
      hostname: 'demo-host',
      os: 'linux',
      arch: 'amd64',
      ip: '10.0.0.1',
      version: '1.0.0',
    },
    stats: createStats(),
    online: true,
    ...overrides,
  };
}

describe('ClientInfoCard', () => {
  test('hides hardware stats for offline clients even when stale stats exist', () => {
    const queryClient = new QueryClient();
    const markup = renderToStaticMarkup(
      createElement(
        QueryClientProvider,
        { client: queryClient },
        createElement(ClientInfoCard, {
          client: createClient({ online: false }),
        }),
      ),
    );

    expect(markup).not.toContain('CPU');
    expect(markup).not.toContain('内存');
    expect(markup).not.toContain('磁盘');
    expect(markup).not.toContain('网络 I/O');
  });

  test('shows delete action only for offline clients when requested', () => {
    const offlineQueryClient = new QueryClient();
    const offlineMarkup = renderToStaticMarkup(
      createElement(
        QueryClientProvider,
        { client: offlineQueryClient },
        createElement(ClientInfoCard, {
          client: createClient({ online: false }),
          onRequestDelete: () => {},
        }),
      ),
    );

    const onlineQueryClient = new QueryClient();
    const onlineMarkup = renderToStaticMarkup(
      createElement(
        QueryClientProvider,
        { client: onlineQueryClient },
        createElement(ClientInfoCard, {
          client: createClient({ online: true }),
          onRequestDelete: () => {},
        }),
      ),
    );

    expect(offlineMarkup).toContain('Delete offline node');
    expect(onlineMarkup).not.toContain('Delete offline node');
  });
});
