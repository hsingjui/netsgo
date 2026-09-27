import { describe, expect, test } from 'vitest';
import { QueryClient } from '@tanstack/react-query';

import { invalidateTunnelQueries } from './use-tunnel-mutations';
import { SELF_RESOURCE_SCOPE } from '@/lib/resource-scope';

describe('invalidateTunnelQueries', () => {
  test('invalidates every tunnel migration dependent cache', async () => {
    const queryClient = new QueryClient();
    const keys = [
      ['users', 'self', 'clients'],
      ['users', 'self', 'client-tunnels', 'old-owner', 'owner'],
      ['users', 'self', 'client-tunnels', 'new-owner', 'owner'],
      ['users', 'self', 'client-traffic', 'old-owner', '60s'],
      ['users', 'self', 'client-traffic', 'new-owner', '24h'],
      ['users', 'self', 'console-summary'],
      ['users', 'self', 'server-status'],
      ['unrelated'],
    ] as const;
    for (const key of keys) {
      queryClient.setQueryData(key, { ready: true });
    }

    invalidateTunnelQueries(queryClient, SELF_RESOURCE_SCOPE);
    await Promise.resolve();

    for (const key of keys.slice(0, -1)) {
      expect(queryClient.getQueryState(key)?.isInvalidated).toBe(true);
    }
    expect(queryClient.getQueryState(keys.at(-1)!)?.isInvalidated).toBe(false);
  });
});
