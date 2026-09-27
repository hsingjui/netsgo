import { describe, expect, test } from 'vitest';

import type { ClientTrafficResponse, ProxyConfig } from '@/types';

import { buildAggregatedTrafficRates, hasTrafficSamples } from './use-traffic-rates';

function isoAt(minuteOffset: number) {
  return new Date(Date.UTC(2026, 3, 19, 10, minuteOffset, 0)).toISOString();
}

const tunnelFilter: Pick<ProxyConfig, 'name' | 'type'>[] = [
  { name: 'api', type: 'tcp' },
];

describe('buildAggregatedTrafficRates', () => {
  test('fills a full 60-point 1h series with zero-rate gaps', () => {
    const data: ClientTrafficResponse = {
      resolution: 'minute',
      items: [
        {
          tunnel_name: 'api',
          tunnel_type: 'tcp',
          points: [
            {
              bucket_start: isoAt(57),
              ingress_bytes: 120,
              egress_bytes: 60,
              total_bytes: 180,
            },
            {
              bucket_start: isoAt(59),
              ingress_bytes: 240,
              egress_bytes: 180,
              total_bytes: 420,
            },
          ],
        },
      ],
    };

    const points = buildAggregatedTrafficRates(data, '1h', undefined, Date.UTC(2026, 3, 19, 10, 59, 30));

    expect(points).toHaveLength(60);
    expect(points[57]).toEqual({
      timestamp: Date.UTC(2026, 3, 19, 10, 57, 0),
      inRate: 2,
      outRate: 1,
    });
    expect(points[58]).toEqual({
      timestamp: Date.UTC(2026, 3, 19, 10, 58, 0),
      inRate: 0,
      outRate: 0,
    });
    expect(points[59]).toEqual({
      timestamp: Date.UTC(2026, 3, 19, 10, 59, 0),
      inRate: 4,
      outRate: 3,
    });
  });

  test('aggregates multiple tunnels into client-level rates', () => {
    const data: ClientTrafficResponse = {
      resolution: 'minute',
      items: [
        {
          tunnel_name: 'api',
          tunnel_type: 'tcp',
          points: [
            {
              bucket_start: isoAt(59),
              ingress_bytes: 120,
              egress_bytes: 60,
              total_bytes: 180,
            },
          ],
        },
        {
          tunnel_name: 'web',
          tunnel_type: 'http',
          points: [
            {
              bucket_start: isoAt(59),
              ingress_bytes: 180,
              egress_bytes: 120,
              total_bytes: 300,
            },
          ],
        },
      ],
    };

    const points = buildAggregatedTrafficRates(data, '1h', undefined, Date.UTC(2026, 3, 19, 10, 59, 30));

    expect(points.at(-1)).toEqual({
      timestamp: Date.UTC(2026, 3, 19, 10, 59, 0),
      inRate: 5,
      outRate: 3,
    });
  });

  test('filters to a single tunnel when a tunnel list is provided', () => {
    const data: ClientTrafficResponse = {
      resolution: 'minute',
      items: [
        {
          tunnel_name: 'api',
          tunnel_type: 'tcp',
          points: [
            {
              bucket_start: isoAt(59),
              ingress_bytes: 300,
              egress_bytes: 120,
              total_bytes: 420,
            },
          ],
        },
        {
          tunnel_name: 'web',
          tunnel_type: 'http',
          points: [
            {
              bucket_start: isoAt(59),
              ingress_bytes: 600,
              egress_bytes: 240,
              total_bytes: 840,
            },
          ],
        },
      ],
    };

    const points = buildAggregatedTrafficRates(data, '1h', tunnelFilter, Date.UTC(2026, 3, 19, 10, 59, 30));

    expect(points.at(-1)).toEqual({
      timestamp: Date.UTC(2026, 3, 19, 10, 59, 0),
      inRate: 5,
      outRate: 2,
    });
  });

  test('filters by tunnel name and type to guard same-name tunnel responses', () => {
    const data: ClientTrafficResponse = {
      resolution: 'minute',
      items: [
        {
          tunnel_name: 'api',
          tunnel_type: 'tcp',
          points: [
            {
              bucket_start: isoAt(59),
              ingress_bytes: 300,
              egress_bytes: 120,
              total_bytes: 420,
            },
          ],
        },
        {
          tunnel_name: 'api',
          tunnel_type: 'udp',
          points: [
            {
              bucket_start: isoAt(59),
              ingress_bytes: 900,
              egress_bytes: 600,
              total_bytes: 1500,
            },
          ],
        },
      ],
    };

    const points = buildAggregatedTrafficRates(data, '1h', tunnelFilter, Date.UTC(2026, 3, 19, 10, 59, 30));

    expect(points.at(-1)).toEqual({
      timestamp: Date.UTC(2026, 3, 19, 10, 59, 0),
      inRate: 5,
      outRate: 2,
    });
  });

  test('filters by tunnel id when traffic metadata includes stable ids', () => {
    const data: ClientTrafficResponse = {
      resolution: 'minute',
      items: [
        {
          tunnel_id: 'tun-a',
          tunnel_name: 'api',
          tunnel_type: 'tcp',
          points: [
            {
              bucket_start: isoAt(59),
              ingress_bytes: 300,
              egress_bytes: 120,
              total_bytes: 420,
            },
          ],
        },
        {
          tunnel_id: 'tun-b',
          tunnel_name: 'api',
          tunnel_type: 'tcp',
          points: [
            {
              bucket_start: isoAt(59),
              ingress_bytes: 900,
              egress_bytes: 600,
              total_bytes: 1500,
            },
          ],
        },
      ],
    };

    const points = buildAggregatedTrafficRates(
      data,
      '1h',
      [{ id: 'tun-a', name: 'api', type: 'tcp' }],
      Date.UTC(2026, 3, 19, 10, 59, 30),
    );

    expect(points.at(-1)).toEqual({
      timestamp: Date.UTC(2026, 3, 19, 10, 59, 0),
      inRate: 5,
      outRate: 2,
    });
  });

  test('keeps historical tunnel data when no tunnel filter is provided', () => {
    const data: ClientTrafficResponse = {
      resolution: 'minute',
      items: [
        {
          tunnel_name: 'deleted-tunnel',
          tunnel_type: 'tcp',
          points: [
            {
              bucket_start: isoAt(59),
              ingress_bytes: 600,
              egress_bytes: 300,
              total_bytes: 900,
            },
          ],
        },
      ],
    };

    const points = buildAggregatedTrafficRates(data, '1h', undefined, Date.UTC(2026, 3, 19, 10, 59, 30));

    expect(points.at(-1)).toEqual({
      timestamp: Date.UTC(2026, 3, 19, 10, 59, 0),
      inRate: 10,
      outRate: 5,
    });
  });

  test('metadata_missing history samples do not crash unfiltered charts', () => {
    const data: ClientTrafficResponse = {
      resolution: 'minute',
      items: [
        {
          tunnel_id: 'deleted-tunnel',
          metadata_missing: true,
          points: [
            {
              bucket_start: isoAt(59),
              ingress_bytes: 120,
              egress_bytes: 60,
              total_bytes: 180,
            },
          ],
        },
      ],
    };

    const points = buildAggregatedTrafficRates(data, '1h', undefined, Date.UTC(2026, 3, 19, 10, 59, 30));

    expect(hasTrafficSamples(data)).toBe(true);
    expect(points.at(-1)).toEqual({
      timestamp: Date.UTC(2026, 3, 19, 10, 59, 0),
      inRate: 2,
      outRate: 1,
    });
  });

  test('aggregates five 1-minute source buckets into one 5-minute grid point for 24h', () => {
    const data: ClientTrafficResponse = {
      resolution: 'minute',
      items: [
        {
          tunnel_name: 'api',
          tunnel_type: 'tcp',
          points: [
            { bucket_start: isoAt(55), ingress_bytes: 300, egress_bytes: 150, total_bytes: 450 },
            { bucket_start: isoAt(56), ingress_bytes: 300, egress_bytes: 150, total_bytes: 450 },
            { bucket_start: isoAt(57), ingress_bytes: 300, egress_bytes: 150, total_bytes: 450 },
            { bucket_start: isoAt(58), ingress_bytes: 300, egress_bytes: 150, total_bytes: 450 },
            { bucket_start: isoAt(59), ingress_bytes: 300, egress_bytes: 150, total_bytes: 450 },
          ],
        },
      ],
    };

    // nowMs 10:59:30 -> endTimestamp 10:59 -> last grid bucket is 10:55 (300_000ms grid)
    const points = buildAggregatedTrafficRates(data, '24h', undefined, Date.UTC(2026, 3, 19, 10, 59, 30));

    expect(points).toHaveLength(24 * 12);
    // Five 1-minute buckets (10:55-10:59) snap into the 10:55 grid bucket.
    // Total ingress 1500 / divisor 300 = 5 bytes/s; egress 750 / 300 = 2.5 -> 2.5
    expect(points.at(-1)).toEqual({
      timestamp: Date.UTC(2026, 3, 19, 10, 55, 0),
      inRate: 5,
      outRate: 2.5,
    });
  });

  test('reports no samples for a successful response with zero points', () => {
    const data: ClientTrafficResponse = {
      resolution: 'minute',
      items: [
        {
          tunnel_name: 'api',
          tunnel_type: 'tcp',
          points: [],
        },
      ],
    };

    expect(hasTrafficSamples(data)).toBe(false);
    expect(hasTrafficSamples(data, tunnelFilter)).toBe(false);
  });
});
