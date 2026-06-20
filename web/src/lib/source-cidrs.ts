function parseSourceCIDRList(value: string) {
  return value.split(',').map((item) => item.trim()).filter(Boolean);
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
