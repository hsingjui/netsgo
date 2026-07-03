import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import {
  forceCollide,
  forceLink,
  forceManyBody,
  forceRadial,
  forceSimulation,
  type Simulation,
  type SimulationLinkDatum,
  type SimulationNodeDatum,
} from 'd3-force';
import { select } from 'd3-selection';
import { zoom, zoomIdentity, type ZoomBehavior } from 'd3-zoom';
import { drag } from 'd3-drag';
import 'd3-transition';
import { useTranslation } from 'react-i18next';
import {
  Undo2,
  Plus,
  Minus,
  LocateFixed,
  Maximize2,
  Minimize2,
} from 'lucide-react';

import { Button } from '@/components/ui/button';
import { cn } from '@/lib/utils';
import {
  SERVER_NODE_ID,
  computeEdgeOffsets,
  computeQuadraticEdge,
  getControlLinkEmphasis,
  getTunnelEdgeEmphasis,
  getTopologyNeighborIds,
  shouldRenderControlLink,
  topologyEdgeTouches,
  type TopologyEdge,
  type TopologyGraph,
  type TopologyNode,
  type TopologyTrafficSnapshot,
  type TopologyViewState,
} from './topology-model';
import {
  EDGE_STROKE,
  LABEL_HALO,
  emphasisOpacity,
  flowDuration,
  formatTrafficPair,
  hasTraffic,
  trafficStrokeWidth,
  truncateLabel,
} from './topology-rendering';
import { TopologyNodeView } from './TopologyNodeView';

interface SimNode extends SimulationNodeDatum {
  id: string;
}

interface SimLink extends SimulationLinkDatum<SimNode> {
  kind: 'control' | 'c2c';
}

function initialNodePosition(
  node: TopologyNode,
  nodes: TopologyNode[],
  pinnedId: string,
  cx: number,
  cy: number,
  radius: number,
) {
  if (node.id === pinnedId) {
    return { x: cx, y: cy };
  }

  const ringNodes = nodes
    .filter((candidate) => candidate.id !== pinnedId)
    .sort((a, b) => a.id.localeCompare(b.id));
  const index = Math.max(0, ringNodes.findIndex((candidate) => candidate.id === node.id));
  const count = Math.max(1, ringNodes.length);
  const startAngle = pinnedId === SERVER_NODE_ID ? -Math.PI / 2 : -Math.PI;
  const angle = startAngle + (index / count) * Math.PI * 2;
  const nodeRadius = node.id === SERVER_NODE_ID ? Math.min(radius * 0.72, 140) : radius;

  return {
    x: cx + Math.cos(angle) * nodeRadius,
    y: cy + Math.sin(angle) * nodeRadius,
  };
}

export function TopologyCanvas({
  graph,
  trafficSnapshot,
  focusId,
  hoveredTunnelId,
  onFocusChange,
  isFullscreen,
  onToggleFullscreen,
}: {
  graph: TopologyGraph;
  trafficSnapshot: TopologyTrafficSnapshot;
  focusId: string | null;
  hoveredTunnelId: string | null;
  onFocusChange: (id: string | null) => void;
  isFullscreen: boolean;
  onToggleFullscreen: () => void;
}) {
  const { t } = useTranslation();
  const containerRef = useRef<HTMLDivElement>(null);
  const svgRef = useRef<SVGSVGElement>(null);
  const sceneRef = useRef<SVGGElement>(null);
  const simRef = useRef<Simulation<SimNode, undefined> | null>(null);
  const nodePosRef = useRef(new Map<string, SimNode>());
  const zoomRef = useRef<ZoomBehavior<SVGSVGElement, unknown> | null>(null);
  const pinnedIdRef = useRef<string>(SERVER_NODE_ID);
  const [size, setSize] = useState({ width: 0, height: 0 });
  const [hoverNodeId, setHoverNodeId] = useState<string | null>(null);
  const [layoutReady, setLayoutReady] = useState(false);

  useLayoutEffect(() => {
    const element = containerRef.current;
    if (!element) return;
    const observer = new ResizeObserver((entries) => {
      const rect = entries[0]?.contentRect;
      if (rect && rect.width > 0 && rect.height > 0) {
        setSize((previous) => {
          if (previous.width === rect.width && previous.height === rect.height) {
            return previous;
          }
          return { width: rect.width, height: rect.height };
        });
      }
    });
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  const effectiveFocusId = focusId !== null
    && focusId !== SERVER_NODE_ID
    && graph.nodes.some((node) => node.id === focusId)
    ? focusId
    : null;

  const visibleNodes = useMemo(() => {
    if (!effectiveFocusId) {
      return graph.nodes;
    }
    const keep = getTopologyNeighborIds(graph, effectiveFocusId);
    keep.add(effectiveFocusId);
    keep.add(SERVER_NODE_ID);
    return graph.nodes.filter((node) => keep.has(node.id));
  }, [graph, effectiveFocusId]);

  const visibleEdges = useMemo(() => {
    const ids = new Set(visibleNodes.map((node) => node.id));
    return graph.edges.filter((edge) => ids.has(edge.sourceId) && ids.has(edge.targetId));
  }, [graph, visibleNodes]);

  const viewState = useMemo<TopologyViewState>(() => ({
    focusId: effectiveFocusId,
    hoverNodeId,
    hoveredTunnelId,
  }), [effectiveFocusId, hoverNodeId, hoveredTunnelId]);

  const controlNodes = useMemo(
    () => visibleNodes.filter((node) => node.kind === 'client' && shouldRenderControlLink(node.id, viewState)),
    [viewState, visibleNodes],
  );

  const edgeOffsets = useMemo(() => computeEdgeOffsets(visibleEdges), [visibleEdges]);

  const tunnelCountByNode = useMemo(() => {
    const counts = new Map<string, number>();
    for (const edge of graph.edges) {
      counts.set(edge.sourceId, (counts.get(edge.sourceId) ?? 0) + 1);
      counts.set(edge.targetId, (counts.get(edge.targetId) ?? 0) + 1);
    }
    return counts;
  }, [graph]);

  // 结构签名：只有拓扑结构真正变化时才重启力导向模拟，
  // 避免周期性的 stats 刷新导致画面持续抖动。
  const structureSignature = useMemo(() => {
    const nodesPart = visibleNodes.map((node) => `${node.id}:${node.online ? 1 : 0}`).join(',');
    const edgesPart = visibleEdges.map((edge) => `${edge.id}:${edge.sourceId}>${edge.targetId}`).join(',');
    return `${nodesPart}|${edgesPart}`;
  }, [visibleNodes, visibleEdges]);

  const visibleNodesRef = useRef(visibleNodes);
  visibleNodesRef.current = visibleNodes;
  const visibleEdgesRef = useRef(visibleEdges);
  visibleEdgesRef.current = visibleEdges;
  const edgeOffsetsRef = useRef(edgeOffsets);
  edgeOffsetsRef.current = edgeOffsets;

  const { width, height } = size;

  // 缩放 / 平移
  useEffect(() => {
    const svgElement = svgRef.current;
    if (!svgElement) return;
    const svg = select(svgElement);
    const behavior = zoom<SVGSVGElement, unknown>()
      .scaleExtent([0.5, 2.5])
      .filter((event: MouseEvent | WheelEvent | TouchEvent) => {
        if (event.type === 'dblclick') return false;
        return !(event as MouseEvent).button;
      })
      .on('zoom', (event) => {
        select(sceneRef.current).attr('transform', event.transform.toString());
      });
    svg.call(behavior);
    zoomRef.current = behavior;
    return () => {
      svg.on('.zoom', null);
      zoomRef.current = null;
    };
  }, []);

  // 力导向模拟
  useLayoutEffect(() => {
    if (!width || !height) return;
    setLayoutReady(false);

    const cx = width / 2;
    const cy = height / 2;
    const ringRadius = Math.max(Math.min(width, height) / 2 - 86, 76);
    const nodePos = nodePosRef.current;

    const nodes = visibleNodesRef.current;
    const edges = visibleEdgesRef.current;
    const pinnedId = effectiveFocusId ?? SERVER_NODE_ID;
    pinnedIdRef.current = pinnedId;
    let disposed = false;
    let sceneSettled = false;

    const simNodes = nodes.map((node) => {
      let sim = nodePos.get(node.id);
      if (!sim) {
        const position = initialNodePosition(node, nodes, pinnedId, cx, cy, ringRadius);
        sim = {
          id: node.id,
          x: position.x,
          y: position.y,
        };
        nodePos.set(node.id, sim);
      }
      sim.fx = null;
      sim.fy = null;
      return sim;
    });
    const pinned = nodePos.get(pinnedId);
    if (pinned) {
      pinned.fx = cx;
      pinned.fy = cy;
    }

    const simLinks: SimLink[] = [];
    for (const node of nodes) {
      if (
        node.kind === 'client'
        && (!effectiveFocusId || node.id === effectiveFocusId)
      ) {
        simLinks.push({ source: SERVER_NODE_ID, target: node.id, kind: 'control' });
      }
    }
    const seenPairs = new Set<string>();
    for (const edge of edges) {
      if (edge.sourceId === SERVER_NODE_ID || edge.targetId === SERVER_NODE_ID) continue;
      const key = edge.sourceId < edge.targetId
        ? `${edge.sourceId}|${edge.targetId}`
        : `${edge.targetId}|${edge.sourceId}`;
      if (seenPairs.has(key)) continue;
      seenPairs.add(key);
      simLinks.push({ source: edge.sourceId, target: edge.targetId, kind: 'c2c' });
    }

    const ticked = () => {
      const scene = sceneRef.current;
      if (!scene) return;
      for (const sim of simNodes) {
        sim.x = Math.max(34, Math.min(width - 34, sim.x ?? cx));
        sim.y = Math.max(38, Math.min(height - 46, sim.y ?? cy));
      }
      const sceneSel = select(scene);
      sceneSel.select('[data-layer="nodes"]')
        .selectAll<SVGGElement, unknown>('[data-node-id]')
        .attr('transform', function positionNode() {
          const sim = nodePos.get(this.dataset.nodeId ?? '');
          return sim ? `translate(${sim.x},${sim.y})` : null;
        });

      const server = nodePos.get(SERVER_NODE_ID);
      sceneSel.selectAll<SVGPathElement, unknown>('[data-control-id]')
        .attr('d', function controlPath() {
          const client = nodePos.get(this.dataset.controlId ?? '');
          if (!client || !server) return null;
          return `M ${server.x} ${server.y} L ${client.x} ${client.y}`;
        });
      sceneSel.selectAll<SVGGElement, unknown>('[data-control-label]')
        .attr('transform', function positionControlLabel() {
          const client = nodePos.get(this.dataset.controlLabel ?? '');
          if (!client || !server) return null;
          const midpointX = ((server.x ?? cx) + (client.x ?? cx)) / 2;
          const midpointY = ((server.y ?? cy) + (client.y ?? cy)) / 2;
          return `translate(${midpointX},${midpointY})`;
        });

      const offsets = edgeOffsetsRef.current;
      const edgeList = visibleEdgesRef.current;
      const geometryById = new Map<string, ReturnType<typeof computeQuadraticEdge>>();
      for (const edge of edgeList) {
        const source = nodePos.get(edge.sourceId);
        const target = nodePos.get(edge.targetId);
        if (!source || !target) continue;
        geometryById.set(edge.id, computeQuadraticEdge(
          edge.sourceId,
          edge.targetId,
          { x: source.x ?? cx, y: source.y ?? cy },
          { x: target.x ?? cx, y: target.y ?? cy },
          offsets.get(edge.id) ?? 0,
        ));
      }
      sceneSel.selectAll<SVGGElement, unknown>('[data-edge-id]').each(function updateEdge() {
        const geometry = geometryById.get(this.dataset.edgeId ?? '');
        if (!geometry) return;
        select(this).selectAll('path').attr('d', geometry.path);
      });
      sceneSel.selectAll<SVGGElement, unknown>('[data-edge-label]')
        .attr('transform', function positionLabel() {
          const geometry = geometryById.get(this.dataset.edgeLabel ?? '');
          return geometry ? `translate(${geometry.midpoint.x},${geometry.midpoint.y})` : null;
        });

      if (!sceneSettled) {
        sceneSettled = true;
        if (!disposed) {
          setLayoutReady(true);
        }
      }
    };

    const simulation = forceSimulation<SimNode>(simNodes)
      .force('charge', forceManyBody<SimNode>().strength(-180))
      .force('collide', forceCollide<SimNode>(46))
      .force('radial', forceRadial<SimNode>(ringRadius, cx, cy).strength(
        (node) => (node.id === pinnedId ? 0 : 0.045),
      ))
      .force('link', forceLink<SimNode, SimLink>(simLinks)
        .id((node) => node.id)
        .distance((link) => (link.kind === 'c2c' ? Math.min(180, ringRadius * 1.15) : ringRadius))
        .strength((link) => (link.kind === 'c2c' ? 0.08 : 0.045)))
      .alpha(0.24)
      .alphaDecay(0.16)
      .velocityDecay(0.65)
      .on('tick', ticked);

    simRef.current = simulation;
    ticked();

    return () => {
      disposed = true;
      simulation.stop();
      simRef.current = null;
    };
  }, [structureSignature, effectiveFocusId, width, height]);

  // 节点拖拽
  useEffect(() => {
    const svgElement = svgRef.current;
    if (!svgElement) return;
    const nodePos = nodePosRef.current;
    const behavior = drag<SVGGElement, unknown>()
      .clickDistance(5)
      .on('start', function dragStart(event) {
        event.sourceEvent?.stopPropagation();
        const sim = nodePos.get(this.dataset.nodeId ?? '');
        if (!sim) return;
        simRef.current?.alphaTarget(0.25).restart();
        sim.fx = sim.x;
        sim.fy = sim.y;
      })
      .on('drag', function dragMove(event) {
        const sim = nodePos.get(this.dataset.nodeId ?? '');
        if (!sim) return;
        sim.fx = event.x;
        sim.fy = event.y;
      })
      .on('end', function dragEnd() {
        simRef.current?.alphaTarget(0);
        const id = this.dataset.nodeId ?? '';
        const sim = nodePos.get(id);
        if (!sim) return;
        if (id !== pinnedIdRef.current) {
          sim.fx = null;
          sim.fy = null;
        }
      });
    select(svgElement)
      .selectAll<SVGGElement, unknown>('[data-node-id]')
      .call(behavior);
  }, [structureSignature]);

  const hoverNeighborIds = useMemo(
    () => (hoverNodeId ? getTopologyNeighborIds(graph, hoverNodeId) : null),
    [graph, hoverNodeId],
  );

  const nodeOpacity = (node: TopologyNode) => {
    if (!hoverNodeId || node.id === hoverNodeId) return 1;
    if (node.id === SERVER_NODE_ID || hoverNeighborIds?.has(node.id)) return 1;
    return 0.3;
  };

  const edgeOpacity = (edge: TopologyEdge) => {
    if (hoveredTunnelId) return edge.id === hoveredTunnelId ? 1 : 0.12;
    if (hoverNodeId) return topologyEdgeTouches(edge, hoverNodeId) ? 1 : 0.15;
    if (effectiveFocusId) return topologyEdgeTouches(edge, effectiveFocusId) ? 0.95 : 0.35;
    return 0.85;
  };

  const zoomBy = (factor: number) => {
    const svgElement = svgRef.current;
    if (!svgElement || !zoomRef.current) return;
    zoomRef.current.scaleBy(select(svgElement).transition().duration(200), factor);
  };

  const resetView = () => {
    const svgElement = svgRef.current;
    if (!svgElement || !zoomRef.current) return;
    zoomRef.current.transform(select(svgElement).transition().duration(280), zoomIdentity);
  };

  return (
    <div
      ref={containerRef}
      className={cn(
        'relative min-w-0 flex-1 overflow-hidden',
        isFullscreen ? 'h-full' : 'h-[340px] sm:h-[460px]',
      )}
    >
      {/* 画布背景 */}
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 bg-white dark:hidden"
      />
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 dark:hidden"
        style={{
          background: '#ffffff',
          backgroundImage: `
            radial-gradient(
              circle at top center,
              rgba(56, 193, 182, 0.25),
              transparent 50%
            )
          `,
          filter: 'blur(200px)',
          backgroundRepeat: 'no-repeat',
        }}
      />
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 hidden bg-black dark:block"
        style={{
          background: 'radial-gradient(ellipse 80% 60% at 50% 0%, rgba(120, 180, 255, 0.5), transparent 70%), #000000',
        }}
      />

      <svg ref={svgRef} className="absolute inset-0 h-full w-full cursor-grab active:cursor-grabbing">
        <defs>
          <filter id="topo-soft" x="-60%" y="-60%" width="220%" height="220%">
            <feGaussianBlur stdDeviation="6" />
          </filter>
        </defs>

        <rect
          width="100%"
          height="100%"
          fill="transparent"
          onClick={() => onFocusChange(null)}
        />

        <g
          ref={sceneRef}
          style={{ opacity: layoutReady ? 1 : 0 }}
        >
          <g data-layer="links">
            {controlNodes.map((node) => {
              const rate = trafficSnapshot.clientRates.get(node.id);
              const emphasis = getControlLinkEmphasis(node.id, viewState);
              const active = hasTraffic(rate);
              return (
                <g
                  key={`control-${node.id}`}
                  className="transition-opacity duration-300"
                  style={{ opacity: emphasisOpacity(emphasis) }}
                >
                  <path
                    data-control-id={node.id}
                    fill="none"
                    strokeWidth={trafficStrokeWidth(rate, emphasis === 'strong' ? 1.35 : 1, emphasis)}
                    strokeDasharray="2 6"
                    strokeLinecap="round"
                    className={cn(
                      'transition-[stroke-width,opacity] duration-300',
                      node.online ? 'stroke-emerald-500/60' : 'stroke-muted-foreground/40',
                    )}
                  />
                  {active && (
                    <path
                      data-control-id={node.id}
                      fill="none"
                      strokeLinecap="round"
                      strokeWidth={trafficStrokeWidth(rate, 2, emphasis)}
                      strokeDasharray="1.5 9"
                      className="stroke-primary"
                      style={{ animation: `topology-flow ${flowDuration(rate)} linear infinite` }}
                    />
                  )}
                </g>
              );
            })}
            {visibleEdges.map((edge) => {
              const emphasis = getTunnelEdgeEmphasis(edge, viewState);
              const rate = trafficSnapshot.tunnelRates.get(edge.id);
              const active = hasTraffic(rate);
              return (
                <g
                  key={edge.id}
                  data-edge-id={edge.id}
                  className="transition-opacity duration-300"
                  style={{ opacity: edgeOpacity(edge) * emphasisOpacity(emphasis) }}
                >
                  <path
                    fill="none"
                    strokeLinecap="round"
                    strokeWidth={trafficStrokeWidth(rate, hoveredTunnelId === edge.id ? 2.4 : 1.6, emphasis)}
                    className={EDGE_STROKE[edge.status.key]}
                  />
                  {active && (
                    <path
                      fill="none"
                      strokeLinecap="round"
                      strokeWidth={trafficStrokeWidth(rate, 2.4, emphasis)}
                      strokeDasharray="1.5 10.5"
                      className={edge.status.key === 'error' ? 'stroke-destructive' : 'stroke-cyan-400'}
                      style={{ animation: `topology-flow ${flowDuration(rate)} linear infinite` }}
                    />
                  )}
                </g>
              );
            })}
          </g>

          <g data-layer="nodes">
            {visibleNodes.map((node) => (
              <TopologyNodeView
                key={node.id}
                node={node}
                focused={effectiveFocusId === node.id}
                tunnelCount={tunnelCountByNode.get(node.id) ?? 0}
                opacity={nodeOpacity(node)}
                onClick={() => onFocusChange(
                  node.id === SERVER_NODE_ID || effectiveFocusId === node.id ? null : node.id,
                )}
                onHover={(hovering) => setHoverNodeId(hovering ? node.id : null)}
              />
            ))}
          </g>

          <g data-layer="labels" className="pointer-events-none">
            {!effectiveFocusId && controlNodes
              .filter((node) => hasTraffic(trafficSnapshot.clientRates.get(node.id)))
              .map((node) => {
                const rate = trafficSnapshot.clientRates.get(node.id);
                const emphasis = getControlLinkEmphasis(node.id, viewState);
                return (
                  <g
                    key={`control-label-${node.id}`}
                    data-control-label={node.id}
                    className="transition-opacity duration-300"
                    style={{ opacity: emphasis === 'strong' ? 1 : 0.35 }}
                  >
                    <text
                      textAnchor="middle"
                      dy={-6}
                      className="fill-primary font-mono text-[9px] transition-opacity duration-300"
                      style={LABEL_HALO}
                    >
                      {formatTrafficPair(rate)}
                    </text>
                  </g>
                );
              })}
            {visibleEdges
              .filter((edge) => {
                const rate = trafficSnapshot.tunnelRates.get(edge.id);
                return getTunnelEdgeEmphasis(edge, viewState) === 'strong'
                  && (hasTraffic(rate) || hoveredTunnelId === edge.id);
              })
              .map((edge) => {
                const rate = trafficSnapshot.tunnelRates.get(edge.id);
                const showTrafficLabel = hasTraffic(rate);
                return (
                  <g key={`edge-label-${edge.id}`} data-edge-label={edge.id}>
                    {showTrafficLabel && (
                      <text
                        textAnchor="middle"
                        dy={-8}
                        className="fill-primary font-mono text-[9px]"
                        style={LABEL_HALO}
                      >
                        {formatTrafficPair(rate)}
                      </text>
                    )}
                    <text
                      textAnchor="middle"
                      dy={showTrafficLabel ? 6 : -5}
                      className="fill-muted-foreground font-mono text-[9px]"
                      style={LABEL_HALO}
                    >
                      {truncateLabel(edge.tunnel.name)}
                    </text>
                  </g>
                );
              })}
          </g>
        </g>
      </svg>

      {/* 返回全览 */}
      <div
        className={cn(
          'absolute left-3 top-3 z-20 transition-all duration-300',
          effectiveFocusId ? 'translate-y-0 opacity-100' : 'pointer-events-none -translate-y-1 opacity-0',
        )}
      >
        <Button
          type="button"
          variant="secondary"
          size="sm"
          className="h-7 gap-1.5 border border-border/40 bg-background/80 px-2.5 text-xs shadow-sm backdrop-blur-sm hover:bg-background"
          onClick={() => onFocusChange(null)}
        >
          <Undo2 className="h-3.5 w-3.5" />
          {t('dashboard.topologyBack')}
        </Button>
      </div>

      {/* 缩放控件 */}
      <div className="absolute bottom-3 right-3 z-20 flex flex-col overflow-hidden rounded-lg border border-border/50 bg-background/80 shadow-sm backdrop-blur-sm">
        <button
          type="button"
          className="flex size-7 items-center justify-center text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground"
          aria-label={t('dashboard.topologyZoomIn')}
          onClick={() => zoomBy(1.3)}
        >
          <Plus className="size-3.5" />
        </button>
        <button
          type="button"
          className="flex size-7 items-center justify-center border-t border-border/40 text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground"
          aria-label={t('dashboard.topologyZoomOut')}
          onClick={() => zoomBy(1 / 1.3)}
        >
          <Minus className="size-3.5" />
        </button>
        <button
          type="button"
          className="flex size-7 items-center justify-center border-t border-border/40 text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground"
          aria-label={t('dashboard.topologyResetView')}
          onClick={resetView}
        >
          <LocateFixed className="size-3.5" />
        </button>
        <button
          type="button"
          className="flex size-7 items-center justify-center border-t border-border/40 text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground"
          aria-label={isFullscreen ? t('dashboard.topologyExitFullscreen') : t('dashboard.topologyFullscreen')}
          onClick={onToggleFullscreen}
        >
          {isFullscreen ? <Minimize2 className="size-3.5" /> : <Maximize2 className="size-3.5" />}
        </button>
      </div>

      {/* 图例 */}
      <div className="pointer-events-none absolute bottom-3 left-3 z-20 flex items-center gap-3 rounded-lg border border-border/40 bg-background/75 px-2.5 py-1.5 text-[10px] text-muted-foreground shadow-sm backdrop-blur-sm">
        <span className="flex items-center gap-1.5">
          <span className="size-1.5 rounded-full bg-emerald-500" />
          {t('tunnels.statusExposed')}
        </span>
        <span className="flex items-center gap-1.5">
          <span className="size-1.5 rounded-full bg-amber-500" />
          {t('tunnels.statusOffline')}
        </span>
        <span className="flex items-center gap-1.5">
          <span className="size-1.5 rounded-full bg-destructive" />
          {t('tunnels.statusError')}
        </span>
        <span className="hidden items-center gap-1.5 sm:flex">
          <svg width="16" height="6" aria-hidden>
            <line x1="0" y1="3" x2="16" y2="3" strokeDasharray="2 4" strokeWidth="1.2" className="stroke-muted-foreground/70" />
          </svg>
          {t('dashboard.topologyControlLink')}
        </span>
      </div>
    </div>
  );
}
