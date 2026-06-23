import type { TunnelFormType, TunnelTopology } from '@/types';

export const DEFAULT_ALLOW_ALL_SOURCE_CIDRS = '0.0.0.0/0, ::/0';
export const DEFAULT_LOOPBACK_SOURCE_CIDRS = '127.0.0.0/8, ::1/128';

function parseSourceCIDRList(value: string) {
  return value.split(',').map((item) => item.trim()).filter(Boolean);
}

function sourceCIDRSetEquals(value: string, expected: string[]) {
  const items = new Set(parseSourceCIDRList(value).map((item) => item.toLowerCase()));
  if (items.size !== expected.length) {
    return false;
  }
  return expected.every((item) => items.has(item.toLowerCase()));
}

export function isDefaultSourceCidrs(value: string) {
  return (
    sourceCIDRSetEquals(value, ['0.0.0.0/0', '::/0'])
    || sourceCIDRSetEquals(value, ['127.0.0.0/8', '::1/128'])
  );
}

export function getDefaultSourceCidrs(type: TunnelFormType, topology: TunnelTopology) {
  if (type === 'socks5' && topology === 'server_expose') {
    return DEFAULT_LOOPBACK_SOURCE_CIDRS;
  }
  return DEFAULT_ALLOW_ALL_SOURCE_CIDRS;
}

export function isDefaultAllowAllSourceCIDRs(items: string[]) {
  const normalized = new Set(items.map((item) => item.toLowerCase()));
  return normalized.has('0.0.0.0/0') && normalized.has('::/0');
}

export function includesLoopbackSourceCIDRs(items: string[]) {
  const normalized = new Set(items.map((item) => item.toLowerCase()));
  return normalized.has('127.0.0.0/8') && normalized.has('::1/128');
}

export function shouldWarnMissingLoopbackSourceCIDRs(value: string) {
  const items = parseSourceCIDRList(value);
  return items.length > 0 && !isDefaultAllowAllSourceCIDRs(items) && !includesLoopbackSourceCIDRs(items);
}

export function preserveLoopbackSourceCIDRsOnFirstRestriction(previousValue: string, nextValue: string) {
  const previousItems = parseSourceCIDRList(previousValue);
  const nextItems = parseSourceCIDRList(nextValue);
  if (
    !isDefaultAllowAllSourceCIDRs(previousItems)
    || nextItems.length === 0
    || isDefaultAllowAllSourceCIDRs(nextItems)
    || includesLoopbackSourceCIDRs(nextItems)
  ) {
    return nextValue;
  }
  return [...nextItems, '127.0.0.0/8', '::1/128'].join(', ');
}
