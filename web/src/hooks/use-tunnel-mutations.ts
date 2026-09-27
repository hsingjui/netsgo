import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import { tunnelApi } from "@/lib/api";
import { invalidateResourceScope, scopedQueryKey, type ResourceScope } from "@/lib/resource-scope";
import {
  buildClientToClientTunnelSpecCreateRequest,
  buildTunnelSpecCreateRequest,
} from "@/lib/tunnel-model";
import type {
  CreateTunnelInput,
  MigrateTunnelInput,
  TransportPolicy,
  TunnelClientRole,
  TunnelTopology,
  UpdateTunnelInput,
} from "@/types";

export function invalidateTunnelQueries(queryClient: QueryClient, scope: ResourceScope) {
  return invalidateResourceScope(queryClient, scope);
}

export function buildClientTunnelRoleQueryKey(
  scope: ResourceScope,
  clientId: string | undefined,
  role = "owner",
) {
  return scopedQueryKey(scope, "client-tunnels", clientId, role);
}

export function useClientTunnelsByRole(
  scope: ResourceScope,
  clientId: string | undefined,
  role: TunnelClientRole,
) {
  return useQuery({
    queryKey: buildClientTunnelRoleQueryKey(scope, clientId, role),
    enabled: Boolean(clientId),
    queryFn: () => {
      if (!clientId) {
        throw new Error("clientId is required to load tunnels by role");
      }
      return tunnelApi.listByClientRole(scope, clientId, role);
    },
    staleTime: 30_000,
  });
}

function buildTunnelSpec(data: {
  topology?: TunnelTopology;
  ingress_client_id?: string;
  bind_ip?: string;
  clientId: string;
  name: string;
  type: CreateTunnelInput["type"];
  local_ip: string;
  local_port: number;
  remote_port?: number;
  domain?: string;
  allowed_source_cidrs?: string[];
  ingress_bps?: number;
  egress_bps?: number;
  total_bps?: number;
  transport_policy?: TransportPolicy;
  socks5?: CreateTunnelInput["socks5"];
  http_auth?: CreateTunnelInput["http_auth"];
  confirm_no_auth_risk?: boolean;
}) {
  if (data.topology === "client_to_client") {
    return buildClientToClientTunnelSpecCreateRequest({
      ingressClientId: data.ingress_client_id ?? "",
      targetClientId: data.clientId,
      name: data.name,
      type: data.type,
      local_ip: data.local_ip,
      local_port: data.local_port,
      remote_port: data.remote_port,
      domain: data.domain,
      allowed_source_cidrs: data.allowed_source_cidrs,
      bind_ip: data.bind_ip ?? "",
      ingress_bps: data.ingress_bps,
      egress_bps: data.egress_bps,
      total_bps: data.total_bps,
      transport_policy: data.transport_policy,
      socks5: data.socks5,
      http_auth: data.http_auth,
      confirm_no_auth_risk: data.confirm_no_auth_risk,
    });
  }
  return buildTunnelSpecCreateRequest(data);
}

export function useCreateTunnel(scope: ResourceScope) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (data: CreateTunnelInput) =>
      tunnelApi.create(scope, buildTunnelSpec(data)),
    onSuccess: () => {
      void invalidateTunnelQueries(queryClient, scope);
    },
  });
}

export function useResumeTunnel(scope: ResourceScope) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ tunnelId }: { tunnelId: string }) =>
      tunnelApi.resume(scope, tunnelId),
    onSuccess: () => {
      void invalidateTunnelQueries(queryClient, scope);
    },
  });
}

export function useStopTunnel(scope: ResourceScope) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ tunnelId }: { tunnelId: string }) =>
      tunnelApi.stop(scope, tunnelId),
    onSuccess: () => {
      void invalidateTunnelQueries(queryClient, scope);
    },
  });
}

export type TunnelBatchAction = "resume" | "stop";

export interface TunnelBatchResult {
  total: number;
  succeeded: number;
  failed: number;
}

export function useBatchTunnelAction(scope: ResourceScope) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async ({
      action,
      tunnelIds,
    }: {
      action: TunnelBatchAction;
      tunnelIds: string[];
    }) => {
      const mutateTunnel =
        action === "resume"
          ? (tunnelId: string) => tunnelApi.resume(scope, tunnelId)
          : (tunnelId: string) => tunnelApi.stop(scope, tunnelId);
      const results = await Promise.allSettled(
        tunnelIds.map((tunnelId) => mutateTunnel(tunnelId)),
      );
      const succeeded = results.filter(
        (result) => result.status === "fulfilled",
      ).length;

      return {
        total: tunnelIds.length,
        succeeded,
        failed: tunnelIds.length - succeeded,
      } satisfies TunnelBatchResult;
    },
    onSettled: () => {
      void invalidateTunnelQueries(queryClient, scope);
    },
  });
}

export function useDeleteTunnel(scope: ResourceScope) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ tunnelId }: { tunnelId: string }) =>
      tunnelApi.delete(scope, tunnelId),
    onSuccess: () => {
      void invalidateTunnelQueries(queryClient, scope);
    },
  });
}

export function useUpdateTunnel(scope: ResourceScope) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (data: UpdateTunnelInput) =>
      tunnelApi.update(scope, data.tunnelId, {
        expected_revision: data.expected_revision,
        spec: buildTunnelSpec(data),
      }),
    onSuccess: () => {
      void invalidateTunnelQueries(queryClient, scope);
    },
  });
}

export function useMigrateTunnel(scope: ResourceScope) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({
      tunnelId,
      expected_revision,
      target_client_id,
    }: MigrateTunnelInput) =>
      tunnelApi.migrate(scope, tunnelId, {
        expected_revision,
        target_client_id,
      }),
    onSuccess: () => {
      void invalidateTunnelQueries(queryClient, scope);
    },
  });
}
