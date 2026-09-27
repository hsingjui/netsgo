import { describe, expect, test } from 'vitest';

import { DEFAULT_LOCALE, LOCALE_STORAGE_KEY, resources, SUPPORTED_LOCALES } from '.';

const mergePreflightErrorCodes = [
  'invalid_api_key',
  'invalid_user_list_query',
  'invalid_user_cursor',
  'invalid_user_id',
  'user_not_found',
  'username_taken',
  'invalid_username',
  'invalid_password',
  'invalid_user_admin_state',
  'invalid_user_status',
  'user_disabled',
  'user_lifecycle_changed',
  'user_mutation_failed',
  'session_revoke_failed',
  'user_must_be_disabled',
  'self_user_lifecycle_forbidden',
  'last_operational_admin',
  'user_disable_incomplete',
  'server_shutting_down',
  'server_owned_field',
  'owner_change_not_supported',
] as const;

describe('i18n', () => {
  test('defaults to English and has resources for every supported locale', () => {
    expect(DEFAULT_LOCALE).toBe('en-US');
    expect(LOCALE_STORAGE_KEY).toBe('netsgo.locale');
    expect(Object.keys(resources).sort()).toEqual([...SUPPORTED_LOCALES].sort());
  });

  test('localizes every merge-preflight user and ownership error', () => {
    for (const locale of SUPPORTED_LOCALES) {
      const errors = resources[locale].translation.errors;
      for (const code of mergePreflightErrorCodes) {
        expect(errors[code], `${locale}: errors.${code}`).toBeTruthy();
      }
    }
  });
});
