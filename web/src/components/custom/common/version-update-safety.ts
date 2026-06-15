export const CANONICAL_UPGRADE_COMMAND = 'curl -fsSL https://netsgo.zs.uy/upgrade.sh | sh -s -- -y';

const FALLBACK_RELEASE_URL = 'https://github.com/zsio/netsgo/releases';

export function safeReleaseURL(value?: string) {
  if (!value) return FALLBACK_RELEASE_URL;
  try {
    const parsed = new URL(value);
    if (parsed.protocol === 'https:' && parsed.hostname === 'github.com' && parsed.pathname.startsWith('/zsio/netsgo/releases')) {
      return parsed.toString();
    }
  } catch {
    // fall through to fallback
  }
  return FALLBACK_RELEASE_URL;
}

export function safeUpgradeCommand(value?: string) {
  return value === CANONICAL_UPGRADE_COMMAND ? value : '';
}
