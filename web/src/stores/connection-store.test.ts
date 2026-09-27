import { beforeEach, describe, expect, test } from 'vitest';

import { useConnectionStore } from './connection-store';
import type { ConnectionStatus } from './connection-store';

describe('useConnectionStore', () => {
  beforeEach(() => {
    useConnectionStore.setState({ status: 'disconnected' });
  });

  test('initial status is disconnected', () => {
    expect(useConnectionStore.getState().status).toBe('disconnected');
  });

  test('transitions through connection lifecycle', () => {
    const transitions: ConnectionStatus[] = ['connecting', 'connected', 'reconnecting', 'disconnected'];

    for (const status of transitions) {
      useConnectionStore.getState().setStatus(status);
      expect(useConnectionStore.getState().status).toBe(status);
    }
  });
});
