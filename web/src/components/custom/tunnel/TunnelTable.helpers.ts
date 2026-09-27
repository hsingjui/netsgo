import { getTunnelActionAvailability } from "@/lib/tunnel-model";
import type { ProxyConfig, TunnelClientRole } from "@/types";

export const CLIENT_DETAIL_TUNNEL_ROLE: TunnelClientRole = "owner";

export function resolveTunnelOwnerClientId(
  proxy: ProxyConfig,
  fallbackClientId: string,
) {
  return proxy.owner_client_id ?? proxy.client_id ?? fallbackClientId;
}

export function getClientOwnedTunnelSource(
  tunnels: ProxyConfig[] | undefined,
  fallbackTunnels: ProxyConfig[] | undefined,
  clientId: string,
) {
  return (tunnels ?? fallbackTunnels ?? []).filter(
    (proxy) => resolveTunnelOwnerClientId(proxy, clientId) === clientId,
  );
}

export function getBatchTunnelIds(
  tunnels: ProxyConfig[],
  action: "resume" | "stop",
) {
  return tunnels
    .filter((tunnel) => {
      const availability = getTunnelActionAvailability(tunnel);
      return action === "resume"
        ? availability.canResume
        : availability.canStop;
    })
    .map((tunnel) => tunnel.id);
}
