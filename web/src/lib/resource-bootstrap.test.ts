import { describe, expect, test } from 'vitest';

import { isResourceBootstrap, parseResourceBootstrapSnapshot } from './resource-bootstrap';

describe('resource bootstrap contract', () => {
  const bootstrap = {
    version: 'v0.2.0',
    server_addr: 'https://netsgo.example.com',
    allowed_ports: [{ start: 10000, end: 20000 }],
  };

  test('extracts the scoped snapshot bootstrap', () => {
    expect(parseResourceBootstrapSnapshot({ bootstrap })).toEqual(bootstrap);
  });

  test('rejects missing or malformed port policy data', () => {
    expect(isResourceBootstrap(undefined)).toBe(false);
    expect(isResourceBootstrap({ ...bootstrap, allowed_ports: undefined })).toBe(false);
    expect(isResourceBootstrap({ ...bootstrap, allowed_ports: [{ start: 0, end: 80 }] })).toBe(false);
    expect(() => parseResourceBootstrapSnapshot({ bootstrap: { ...bootstrap, version: 2 } })).toThrow();
  });
});
