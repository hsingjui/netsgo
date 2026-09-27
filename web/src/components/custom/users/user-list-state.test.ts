import { describe, expect, test } from 'vitest';

import {
  canSubmitUserTextAction,
  hasUserActionCapability,
  userDeletionImpactTotal,
} from './user-list-state';

describe('user list action state', () => {
  test('password reset requires only the visible password field', () => {
    expect(canSubmitUserTextAction('password', '', 'new-secret')).toBe(true);
    expect(canSubmitUserTextAction('password', '', '')).toBe(false);
  });

  test('create and rename retain their username requirements', () => {
    expect(canSubmitUserTextAction('create', 'alice', 'secret')).toBe(true);
    expect(canSubmitUserTextAction('create', '', 'secret')).toBe(false);
    expect(canSubmitUserTextAction('create', 'alice', '')).toBe(false);
    expect(canSubmitUserTextAction('rename', '  alice  ', '')).toBe(true);
    expect(canSubmitUserTextAction('rename', '  ', 'unused')).toBe(false);
  });

  test('missing or partial action capabilities fail closed', () => {
    expect(hasUserActionCapability(undefined, 'can_delete')).toBe(false);
    expect(hasUserActionCapability({}, 'can_delete')).toBe(false);
    expect(hasUserActionCapability({ can_delete: false }, 'can_delete')).toBe(false);
    expect(hasUserActionCapability({ can_delete: true }, 'can_delete')).toBe(true);
  });

  test('sums every count shown by the deletion impact dialog', () => {
    expect(userDeletionImpactTotal({
      user_id: 'user-a',
      api_keys: 1,
      clients: 2,
      tunnels: 3,
      traffic_buckets: 4,
      activity_events: 5,
      generated_at: '2026-08-09T00:00:00Z',
    })).toBe(15);
  });
});
