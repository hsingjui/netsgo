import { useMemo, useState } from "react";
import {
  ArrowRightLeft,
  Ellipsis,
  GitBranchPlus,
  Pause,
  Play,
} from "lucide-react";
import toast from "react-hot-toast";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  TunnelListTable,
  type TunnelEntry,
} from "@/components/custom/tunnel/TunnelListTable";
import { TunnelDialog } from "@/components/custom/tunnel/TunnelDialog";
import { useClientTraffic } from "@/hooks/use-client-traffic";
import {
  useBatchTunnelAction,
  useClientTunnelsByRole,
  type TunnelBatchAction,
} from "@/hooks/use-tunnel-mutations";
import type { Client } from "@/types";
import type { ResourceScope } from "@/lib/resource-scope";
import { getClientDisplayName } from "@/lib/client-utils";
import {
  getTrafficSeriesKey,
  getTunnelSeriesKey,
} from "@/lib/tunnel-traffic-keys";
import { useTranslation } from "react-i18next";
import {
  CLIENT_DETAIL_TUNNEL_ROLE,
  getBatchTunnelIds,
  getClientOwnedTunnelSource,
  resolveTunnelOwnerClientId,
} from "@/components/custom/tunnel/TunnelTable.helpers";

interface TunnelTableProps {
  scope: ResourceScope;
  client: Client;
  clients?: Client[];
}

export function TunnelTable({ scope, client, clients = [client] }: TunnelTableProps) {
  const { t } = useTranslation();
  const [createOpen, setCreateOpen] = useState(false);
  const batchTunnelAction = useBatchTunnelAction(scope);
  const {
    data: trafficData,
    isLoading: isTraffic24hLoading,
    isError: isTraffic24hError,
  } = useClientTraffic(scope, client.id, "24h");
  const { data: ownerTunnels } = useClientTunnelsByRole(
    scope,
    client.id,
    CLIENT_DETAIL_TUNNEL_ROLE,
  );

  const traffic24hByTunnel = useMemo(() => {
    const totals = new Map<string, number>();

    for (const item of trafficData?.items ?? []) {
      totals.set(
        getTrafficSeriesKey(item),
        item.points.reduce((sum, point) => sum + point.total_bytes, 0),
      );
    }

    return totals;
  }, [trafficData?.items]);

  const tunnelSource = getClientOwnedTunnelSource(
    ownerTunnels,
    client.proxies,
    client.id,
  );
  const tunnels: TunnelEntry[] = tunnelSource.map((proxy) => ({
    ...proxy,
    clientId: resolveTunnelOwnerClientId(proxy, client.id),
    clientName: getClientDisplayName(client),
    clientOnline: client.online,
    traffic24hBytes: trafficData
      ? (traffic24hByTunnel.get(getTunnelSeriesKey(proxy)) ?? 0)
      : undefined,
  }));
  const resumableTunnelIds = getBatchTunnelIds(tunnels, "resume");
  const stoppableTunnelIds = getBatchTunnelIds(tunnels, "stop");

  const runBatchAction = (action: TunnelBatchAction, tunnelIds: string[]) => {
    batchTunnelAction.mutate(
      { action, tunnelIds },
      {
        onSuccess: ({ succeeded, failed }) => {
          if (failed > 0) {
            toast.error(t("tunnels.batchPartial", { succeeded, failed }));
            return;
          }
          toast.success(
            action === "resume"
              ? t("tunnels.batchStarted", { count: succeeded })
              : t("tunnels.batchStopped", { count: succeeded }),
          );
        },
        onError: (error) => toast.error((error as Error).message),
      },
    );
  };

  return (
    <>
      <TunnelListTable
        scope={scope}
        tunnels={tunnels}
        clients={clients}
        title={t("dashboard.childTunnels")}
        icon={<ArrowRightLeft className="h-5 w-5 text-primary" />}
        showClient={false}
        showTraffic24h
        traffic24hState={
          isTraffic24hError
            ? "error"
            : isTraffic24hLoading
              ? "loading"
              : "ready"
        }
        showActions
        showSearch={false}
        headerAction={
          <div className="flex items-center gap-2">
            <Button
              type="button"
              variant="outline"
              onClick={() => setCreateOpen(true)}
            >
              <GitBranchPlus data-icon="inline-start" />
              {t("tunnels.addTunnel")}
            </Button>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  aria-label={t("tunnels.moreActions")}
                  disabled={tunnels.length === 0 || batchTunnelAction.isPending}
                >
                  <Ellipsis />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="min-w-36">
                <DropdownMenuGroup>
                  <DropdownMenuItem
                    disabled={resumableTunnelIds.length === 0}
                    onSelect={() =>
                      runBatchAction("resume", resumableTunnelIds)
                    }
                  >
                    <Play />
                    {t("tunnels.startAll")}
                  </DropdownMenuItem>
                  <DropdownMenuItem
                    disabled={stoppableTunnelIds.length === 0}
                    onSelect={() => runBatchAction("stop", stoppableTunnelIds)}
                  >
                    <Pause />
                    {t("tunnels.stopAll")}
                  </DropdownMenuItem>
                </DropdownMenuGroup>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        }
        emptyAction={
          <Button
            type="button"
            variant="outline"
            className="mt-4"
            onClick={() => setCreateOpen(true)}
          >
            <GitBranchPlus data-icon="inline-start" />
            {t("dashboard.createNow")}
          </Button>
        }
      />
      <TunnelDialog
        scope={scope}
        mode="create"
        clientId={client.id}
        clients={clients}
        open={createOpen}
        onOpenChange={setCreateOpen}
        hideTrigger
      />
    </>
  );
}
