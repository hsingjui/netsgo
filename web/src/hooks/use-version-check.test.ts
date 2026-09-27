import { describe, expect, test } from 'vitest';

import { versionCheckQueryKey } from './use-version-check';
import { SELF_RESOURCE_SCOPE } from '@/lib/resource-scope';

describe('versionCheckQueryKey', () => {
  test('includes target, version, install method, os and arch', () => {
    expect([...versionCheckQueryKey(SELF_RESOURCE_SCOPE, {
      kind: 'client',
      id: 'client-1',
      version: 'v0.1.0',
      installMethod: 'service',
      os: 'linux',
      arch: 'amd64',
    })]).toEqual([
      'users',
      'self',
      'version-check',
      'client',
      'client-1',
      'v0.1.0',
      'service',
      'linux',
      'amd64',
    ]);
  });

  test('falls back to binary install method for missing capability', () => {
    expect([...versionCheckQueryKey(SELF_RESOURCE_SCOPE, {
      kind: 'server',
      version: 'v0.1.0',
    })]).toEqual([
      'users',
      'self',
      'version-check',
      'server',
      'server',
      'v0.1.0',
      'binary',
      '',
      '',
    ]);
  });
});
