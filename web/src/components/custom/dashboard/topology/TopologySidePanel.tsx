import { useNavigate } from '@tanstack/react-router';
import { MoveRight } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { buildTunnelViewModel } from '@/lib/tunnel-model';
import { cn } from '@/lib/utils';
import {
  topologyEdgeTouches,
  type TopologyEdge,
  type TopologyGraph,
  type TopologyTrafficRate,
  type TopologyTrafficSnapshot,
} from './topology-model';
import {
  STATUS_DOT,
  STATUS_TEXT,
  formatTrafficPair,
  hasTraffic,
  statusLabel,
} from './topology-rendering';

export function TopologySidePanel({
  graph,
  trafficSnapshot,
  focusId,
  hoveredTunnelId,
  onHoverTunnel,
}: {
  graph: TopologyGraph;
  trafficSnapshot: TopologyTrafficSnapshot;
  focusId: string;
  hoveredTunnelId: string | null;
  onHoverTunnel: (id: string | null) => void;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();

  const focusNode = graph.nodes.find((node) => node.id === focusId);
  if (!focusNode || focusNode.kind !== 'client') {
    return null;
  }

  const relatedEdges = graph.edges.filter((edge) => topologyEdgeTouches(edge, focusNode.id));

  return (
    <div className="flex w-full shrink-0 flex-col border-t border-border/40 lg:h-full lg:min-h-0 lg:w-80 lg:overflow-hidden lg:border-l lg:border-t-0">
      {relatedEdges.length === 0 ? (
        <p className="px-4 py-6 text-center text-xs text-muted-foreground">
          {t('dashboard.topologyNoTunnels')}
        </p>
      ) : (
        <div className="max-h-72 min-h-0 flex-1 overflow-y-auto px-2 py-2 [scrollbar-width:thin] lg:max-h-none">
          {relatedEdges.map((edge) => (
            <TunnelListItem
              key={edge.id}
              edge={edge}
              graph={graph}
              trafficRate={trafficSnapshot.tunnelRates.get(edge.id)}
              hovered={hoveredTunnelId === edge.id}
              onHover={onHoverTunnel}
              onNavigate={() => navigate({
                to: '/dashboard/clients/$clientId',
                params: { clientId: edge.tunnel.client_id },
              })}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function TunnelListItem({
  edge,
  graph,
  trafficRate,
  hovered,
  onHover,
  onNavigate,
}: {
  edge: TopologyEdge;
  graph: TopologyGraph;
  trafficRate: TopologyTrafficRate | undefined;
  hovered: boolean;
  onHover: (id: string | null) => void;
  onNavigate: () => void;
}) {
  const { t } = useTranslation();
  const view = buildTunnelViewModel(edge.tunnel, true);
  const sourceNode = graph.nodes.find((node) => node.id === edge.sourceId);
  const targetNode = graph.nodes.find((node) => node.id === edge.targetId);
  const sourceName = sourceNode?.kind === 'server' ? t('dashboard.topologyServer') : sourceNode?.label ?? '-';
  const targetName = targetNode?.kind === 'server' ? t('dashboard.topologyServer') : targetNode?.label ?? '-';

  return (
    <button
      type="button"
      className={cn(
        'flex w-full flex-col gap-1 rounded-lg border border-transparent px-2.5 py-2 text-left transition-colors',
        hovered ? 'border-border/40 bg-muted/50' : 'hover:border-border/40 hover:bg-muted/40',
      )}
      onMouseEnter={() => onHover(edge.id)}
      onMouseLeave={() => onHover(null)}
      onFocus={() => onHover(edge.id)}
      onBlur={() => onHover(null)}
      onClick={onNavigate}
      title={`${view.routeLabel} · ${statusLabel(t, edge.status)}`}
    >
      <span className="flex min-w-0 items-center gap-1.5">
        <span className={cn('size-1.5 shrink-0 rounded-full', STATUS_DOT[edge.status.key])} />
        <span className="min-w-0 truncate text-xs font-medium text-foreground">{edge.tunnel.name}</span>
        <span className="shrink-0 rounded border border-border/50 bg-muted/30 px-1 text-[9px] leading-3.5 font-medium text-muted-foreground">
          {edge.tunnel.type.toUpperCase()}
        </span>
        <span className={cn('ml-auto shrink-0 text-[10px]', STATUS_TEXT[edge.status.key])}>
          {statusLabel(t, edge.status)}
        </span>
      </span>
      <span className="flex min-w-0 items-center gap-1 font-mono text-[10px] leading-4 text-muted-foreground">
        <span className="truncate" title={`${sourceName} ${view.targetLabel}`}>{sourceName}</span>
        <MoveRight className="size-3 shrink-0 text-emerald-500/70" />
        <span className="truncate" title={`${targetName} ${view.destinationLabel}`}>{targetName}</span>
      </span>
      <span className="block min-w-0 truncate font-mono text-[10px] leading-4 text-primary/70">
        {view.targetLabel} → {view.destinationLabel}
      </span>
      <span className={cn(
        'block min-w-0 truncate font-mono text-[10px] leading-4',
        hasTraffic(trafficRate) ? 'text-primary' : 'text-muted-foreground',
      )}>
        {formatTrafficPair(trafficRate)}
      </span>
    </button>
  );
}
