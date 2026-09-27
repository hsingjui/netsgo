import type { ResourceBootstrap } from '@/types';

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isPortRange(value: unknown) {
  return isRecord(value)
    && typeof value.start === 'number'
    && typeof value.end === 'number'
    && Number.isInteger(value.start)
    && Number.isInteger(value.end)
    && value.start >= 1
    && value.end >= value.start
    && value.end <= 65535;
}

export function isResourceBootstrap(value: unknown): value is ResourceBootstrap {
  return isRecord(value)
    && typeof value.version === 'string'
    && typeof value.server_addr === 'string'
    && Array.isArray(value.allowed_ports)
    && value.allowed_ports.every(isPortRange);
}

export function parseResourceBootstrapSnapshot(value: unknown): ResourceBootstrap {
  if (!isRecord(value) || !isResourceBootstrap(value.bootstrap)) {
    throw new Error('invalid resource bootstrap response');
  }
  return value.bootstrap;
}
