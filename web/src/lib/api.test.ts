import { describe, expect, test } from 'vitest';

import { parseUserDeletionImpact, shouldLogoutOnAPIError, tunnelApi, usersApi } from './api';
import { SELF_RESOURCE_SCOPE } from '@/lib/resource-scope';

describe('shouldLogoutOnAPIError', () => {
  test('logs out when the server reports an expired or missing session', () => {
    expect(shouldLogoutOnAPIError(401, 'missing_credentials')).toBe(true);
    expect(shouldLogoutOnAPIError(401, 'session_expired_or_revoked')).toBe(true);
    expect(shouldLogoutOnAPIError(401, undefined)).toBe(true);
  });

  test('keeps the current page for credential verification errors', () => {
    expect(shouldLogoutOnAPIError(401, 'current_password_incorrect')).toBe(false);
    expect(shouldLogoutOnAPIError(401, 'invalid_mfa_code')).toBe(false);
    expect(shouldLogoutOnAPIError(401, 'passkey_login_failed')).toBe(false);
  });

  test('ignores non-auth statuses', () => {
    expect(shouldLogoutOnAPIError(400, 'invalid_request_body')).toBe(false);
    expect(shouldLogoutOnAPIError(500, 'temporary_storage_failure')).toBe(false);
  });
});

describe('tunnelApi.migrate', () => {
  test('posts the current revision and target client to the encoded migrate route', async () => {
    const originalFetch = globalThis.fetch;
    let capturedUrl = '';
    let capturedInit: RequestInit | undefined;
    globalThis.fetch = (async (input, init) => {
      capturedUrl = String(input);
      capturedInit = init;
      return new Response(JSON.stringify({ success: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    }) as typeof fetch;

    try {
      await tunnelApi.migrate(SELF_RESOURCE_SCOPE, 'tunnel/with space', {
        expected_revision: 12,
        target_client_id: 'client-next',
      });
    } finally {
      globalThis.fetch = originalFetch;
    }

    expect(capturedUrl).toBe('/api/tunnels/tunnel%2Fwith%20space/migrate');
    expect(capturedInit?.method).toBe('POST');
    expect(JSON.parse(String(capturedInit?.body))).toEqual({
      expected_revision: 12,
      target_client_id: 'client-next',
    });
  });
});

describe('usersApi.deletionImpact', () => {
  test('loads the encoded deletion-impact endpoint and validates every count', async () => {
    const originalFetch = globalThis.fetch;
    let capturedUrl = '';
    globalThis.fetch = (async (input) => {
      capturedUrl = String(input);
      return new Response(JSON.stringify({
        user_id: 'user/with space',
        api_keys: 1,
        clients: 2,
        tunnels: 3,
        traffic_buckets: 4,
        activity_events: 5,
        generated_at: '2026-08-09T00:00:00Z',
      }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    }) as typeof fetch;

    try {
      await expect(usersApi.deletionImpact('user/with space')).resolves.toMatchObject({
        clients: 2,
        activity_events: 5,
      });
    } finally {
      globalThis.fetch = originalFetch;
    }

    expect(capturedUrl).toBe('/api/admin/users/user%2Fwith%20space/deletion-impact');
  });

  test('fails closed for missing, negative, fractional, or invalid timestamp fields', () => {
    const valid = {
      user_id: 'user-a',
      api_keys: 1,
      clients: 2,
      tunnels: 3,
      traffic_buckets: 4,
      activity_events: 5,
      generated_at: '2026-08-09T00:00:00Z',
    };
    expect(() => parseUserDeletionImpact({ ...valid, clients: -1 })).toThrow();
    expect(() => parseUserDeletionImpact({ ...valid, tunnels: 1.5 })).toThrow();
    expect(() => parseUserDeletionImpact({ ...valid, activity_events: undefined })).toThrow();
    expect(() => parseUserDeletionImpact({ ...valid, user_id: '' })).toThrow();
    expect(() => parseUserDeletionImpact({ ...valid, user_id: '   ' })).toThrow();
    expect(() => parseUserDeletionImpact(valid, 'user-b')).toThrow();
    expect(() => parseUserDeletionImpact({ ...valid, generated_at: 'not-a-time' })).toThrow();
    expect(() => parseUserDeletionImpact({ ...valid, generated_at: '2026-08-09' })).toThrow();
  });
});
