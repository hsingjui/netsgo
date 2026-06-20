import { describe, expect, test } from 'bun:test';

import { ApiError } from '@/lib/api';

import {
  canFallbackToLegacyTunnelEndpoint,
  shouldUseLegacyTunnelEndpoint,
} from './use-tunnel-mutations';

describe('shouldUseLegacyTunnelEndpoint', () => {
  test('allows legacy fallback for old server-expose endpoints', () => {
    expect(shouldUseLegacyTunnelEndpoint(new ApiError(404, 'Not Found'), 'server_expose')).toBe(true);
    expect(shouldUseLegacyTunnelEndpoint(new ApiError(405, 'Method Not Allowed'))).toBe(true);
  });

  test('does not fallback for client-to-client mutations', () => {
    expect(shouldUseLegacyTunnelEndpoint(new ApiError(404, 'Not Found'), 'client_to_client')).toBe(false);
  });

  test('does not fallback for non endpoint-missing errors', () => {
    expect(shouldUseLegacyTunnelEndpoint(new ApiError(500, 'Internal Server Error'), 'server_expose')).toBe(false);
    expect(shouldUseLegacyTunnelEndpoint(new Error('network failed'), 'server_expose')).toBe(false);
  });
});

describe('canFallbackToLegacyTunnelEndpoint', () => {
  test('allows fallback only when no unified-only security config would be dropped', () => {
    expect(canFallbackToLegacyTunnelEndpoint(
      { type: 'http', topology: 'server_expose' },
      new ApiError(404, 'Not Found'),
    )).toBe(true);
    expect(canFallbackToLegacyTunnelEndpoint(
      { type: 'http', topology: 'server_expose', allowed_source_cidrs: ['0.0.0.0/0', '::/0'] },
      new ApiError(404, 'Not Found'),
    )).toBe(true);
  });

  test('does not fallback when HTTP auth or custom source CIDRs would be lost', () => {
    expect(canFallbackToLegacyTunnelEndpoint(
      { type: 'http', topology: 'server_expose', http_auth: { enabled: true, username: 'alice', password: 'secret' } },
      new ApiError(404, 'Not Found'),
    )).toBe(false);
    expect(canFallbackToLegacyTunnelEndpoint(
      { type: 'tcp', topology: 'server_expose', allowed_source_cidrs: ['203.0.113.0/24'] },
      new ApiError(404, 'Not Found'),
    )).toBe(false);
  });
});
