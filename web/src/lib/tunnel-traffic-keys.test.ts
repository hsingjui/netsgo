import { describe, expect, test } from 'vitest';

import { getTrafficSeriesKey, getTunnelSeriesKey } from './tunnel-traffic-keys';

describe('tunnel traffic keys', () => {
  test('uses stable tunnel ids when present', () => {
    expect(getTunnelSeriesKey({ id: 'tun-1', name: 'api', type: 'tcp' })).toBe('id:tun-1');
    expect(getTrafficSeriesKey({
      tunnel_id: 'tun-1',
      tunnel_name: 'api',
      tunnel_type: 'tcp',
      points: [],
    })).toBe('id:tun-1');
  });

  test('falls back to type and name only when ids are absent', () => {
    expect(getTunnelSeriesKey({ name: 'api', type: 'tcp' })).toBe('tcp:api');
    expect(getTrafficSeriesKey({
      tunnel_name: 'api',
      tunnel_type: 'tcp',
      points: [],
    })).toBe('tcp:api');
  });

  test('uses partial metadata so distinct orphan series do not collapse', () => {
    expect(getTrafficSeriesKey({ tunnel_name: 'api', points: [] })).toBe('name:api');
    expect(getTrafficSeriesKey({ tunnel_type: 'tcp', points: [] })).toBe('type:tcp');
    expect(getTrafficSeriesKey({ points: [] })).toBe('metadata_missing');
  });
});
