import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { scopedClientPath } from '@/lib/api';
import { scopedQueryKey, type ResourceScope } from '@/lib/resource-scope';
import type { ClientTrafficRange, ClientTrafficResponse, TrafficResolution } from '@/types';

export interface UseClientTrafficOptions {
  tunnel?: string;
}

const TRAFFIC_RANGE_CONFIG: Record<
  ClientTrafficRange,
  { durationSeconds: number; resolution: TrafficResolution; refetchInterval: number }
> = {
  '60s': {
    durationSeconds: 60,
    resolution: 'second',
    refetchInterval: 10_000,
  },
  '1h': {
    durationSeconds: 60 * 60,
    resolution: 'minute',
    refetchInterval: 30_000,
  },
  '24h': {
    durationSeconds: 24 * 60 * 60,
    resolution: 'minute',
    refetchInterval: 60_000,
  },
  '7d': {
    durationSeconds: 7 * 24 * 60 * 60,
    resolution: 'hour',
    refetchInterval: 5 * 60_000,
  },
};

export function buildClientTrafficQueryKey(
  scope: ResourceScope,
  clientId: string | undefined,
  range: ClientTrafficRange,
  options: UseClientTrafficOptions = {},
) {
  return scopedQueryKey(scope, 'client-traffic', clientId, range, options.tunnel ?? '');
}

export function buildClientTrafficUrl(
  scope: ResourceScope,
  clientId: string,
  range: ClientTrafficRange,
  options: UseClientTrafficOptions = {},
  nowSeconds = Math.floor(Date.now() / 1000),
) {
  const config = TRAFFIC_RANGE_CONFIG[range];
  const toSeconds = range === '60s' ? nowSeconds - 1 : nowSeconds;
  const fromSeconds = range === '60s'
    ? toSeconds - (config.durationSeconds - 1)
    : toSeconds - config.durationSeconds;
  const params = new URLSearchParams({
    from: String(fromSeconds),
    to: String(toSeconds),
    resolution: config.resolution,
  });

  if (options.tunnel) {
    params.set('tunnel', options.tunnel);
  }

  return `${scopedClientPath(scope, clientId, '/traffic')}?${params.toString()}`;
}

export function useClientTraffic(
  scope: ResourceScope,
  clientId: string | undefined,
  range: ClientTrafficRange,
  options: UseClientTrafficOptions = {},
) {
  const config = TRAFFIC_RANGE_CONFIG[range];

  return useQuery({
    queryKey: buildClientTrafficQueryKey(scope, clientId, range, options),
    enabled: Boolean(clientId),
    queryFn: async () => {
      if (!clientId) {
        throw new Error('clientId is required to load traffic data');
      }

      return api.get<ClientTrafficResponse>(buildClientTrafficUrl(scope, clientId, range, options));
    },
    staleTime: 30_000,
    refetchInterval: config.refetchInterval,
    refetchOnWindowFocus: false,
  });
}
