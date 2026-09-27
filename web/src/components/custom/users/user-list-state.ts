import type { UserActionCapabilities, UserDeletionImpact } from '@/types';

export type UserTextActionKind = 'create' | 'rename' | 'password';

export function canSubmitUserTextAction(
  kind: UserTextActionKind | undefined,
  username: string,
  password: string,
) {
  if (kind === 'create') return username.trim().length > 0 && password.length > 0;
  if (kind === 'rename') return username.trim().length > 0;
  if (kind === 'password') return password.length > 0;
  return false;
}

export function hasUserActionCapability(
  actions: UserActionCapabilities | undefined,
  capability: keyof UserActionCapabilities,
) {
  return actions?.[capability] === true;
}

export function userDeletionImpactTotal(impact: UserDeletionImpact) {
  return impact.api_keys
    + impact.clients
    + impact.tunnels
    + impact.traffic_buckets
    + impact.activity_events;
}
