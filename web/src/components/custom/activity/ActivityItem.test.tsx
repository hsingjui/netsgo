import { describe, expect, test } from 'vitest';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';

import { ActivityItem } from './ActivityItem';
import { i18n } from '@/i18n';
import { TooltipProvider } from '@/components/ui/tooltip';
import type { ActivityItem as ActivityItemType } from '@/types';

function item(overrides: Partial<ActivityItemType> = {}): ActivityItemType {
  return {
    id: 1,
    occurred_at: '2026-07-23T00:00:00Z',
    recorded_at: '2026-07-23T00:00:00Z',
    severity: 'warning',
    category: 'security',
    action: 'client_auth_failed',
    source: 'server',
    actor: { type: 'security' },
    payload_version: 1,
    payload: { summary_key: 'activity.security.client_auth_failed', reason_code: 'invalid_token' },
    clients: [],
    tunnels: [],
    ...overrides,
  };
}

function render(activity: ActivityItemType) {
  const client = new QueryClient();
  return renderToStaticMarkup(createElement(QueryClientProvider, { client }, createElement(TooltipProvider, null, createElement(ActivityItem, { item: activity }))));
}

describe('ActivityItem', () => {
  test('renders allowlisted localized summary without arbitrary payload fields', async () => {
    await i18n.changeLanguage('en-US');
    const markup = render(item({ payload: {
      summary_key: 'activity.security.client_auth_failed',
      reason_code: 'invalid_token',
      session_id: '<script>secret-session</script>',
    } }));
    expect(markup).toContain('Client authentication failed');
    expect(markup).toContain('The token was invalid.');
    expect(markup).not.toContain('secret-session');
    expect(markup).not.toContain('&lt;script&gt;');
  });

  test('unknown payload versions use a generic safe summary', async () => {
    await i18n.changeLanguage('en-US');
    const markup = render(item({ payload_version: 99, payload: { summary_key: 'activity.security.client_auth_failed' } }));
    expect(markup).toContain('Details are unavailable for this event version.');
    expect(markup).not.toContain('Client authentication failed');
  });

  test('renders the Chinese summary and reason', async () => {
    await i18n.changeLanguage('zh-CN');
    const markup = render(item());
    expect(markup).toContain('客户端认证失败');
    expect(markup).toContain('令牌无效');
    await i18n.changeLanguage('en-US');
  });

  test('renders the lifecycle convergence warning in both locales', async () => {
    const warning = item({
      category: 'admin',
      action: 'user_convergence_incomplete',
      payload: {
        summary_key: 'activity.admin.user_convergence_incomplete',
        summary_args: { resource_name: 'alice' },
      },
    });

    await i18n.changeLanguage('en-US');
    expect(render(warning)).toContain('Runtime cleanup for user alice did not fully converge');
    await i18n.changeLanguage('zh-CN');
    expect(render(warning)).toContain('用户 alice 的运行态清理未完全收敛');
    await i18n.changeLanguage('en-US');
  });

  test('shows the event type beside the time and never exposes raw resource IDs', async () => {
    await i18n.changeLanguage('en-US');
    const markup = render(item({
      category: 'p2p',
      action: 'connected',
      actor: { type: 'client', id: 'actor-opaque-id', name: 'actor-opaque-id' },
      payload: { summary_key: 'activity.p2p.connected' },
      clients: [{ client_id: 'client-opaque-id', relation: 'peer' }],
      tunnels: [{ tunnel_id: 'tunnel-opaque-id', relation: 'shared_session' }],
    }));

    expect(markup).toContain('data-slot="badge"');
    expect(markup).toContain('>P2P</span>');
    expect(markup).toContain('Participant client');
    expect(markup).toContain('Unknown client');
    expect(markup).toContain('Tunnel: ');
    expect(markup).toContain('Unknown tunnel');
    expect(markup).not.toContain('actor-opaque-id');
    expect(markup).not.toContain('client-opaque-id');
    expect(markup).not.toContain('tunnel-opaque-id');
    expect(markup).not.toContain('#0001');
  });

  test('uses captured display names when an older summary contains a client ID', async () => {
    await i18n.changeLanguage('en-US');
    const markup = render(item({
      severity: 'info',
      category: 'client',
      action: 'online',
      actor: { type: 'client', id: 'client-opaque-id', name: 'client-opaque-id' },
      payload: {
        summary_key: 'activity.client.online',
        summary_args: { client_name: 'client-opaque-id' },
      },
      clients: [{
        client_id: 'client-opaque-id',
        relation: 'subject',
        display_name: 'Office Mac',
        hostname: 'original-hostname',
      }],
    }));

    expect(markup).toContain('Office Mac came online');
    expect(markup).not.toContain('client-opaque-id');
    expect(markup).not.toContain('original-hostname');
  });

  test('uses the current client name even when the historical payload has a readable name', async () => {
    await i18n.changeLanguage('en-US');
    const markup = render(item({
      severity: 'info',
      category: 'client',
      action: 'online',
      actor: { type: 'client', id: 'client-id', name: 'Old device name' },
      payload: {
        summary_key: 'activity.client.online',
        summary_args: { client_name: 'Old device name' },
      },
      clients: [{ client_id: 'client-id', relation: 'subject', display_name: 'Current device name' }],
    }));

    expect(markup).toContain('Current device name came online');
    expect(markup).not.toContain('Old device name');
  });

  test('uses the current tunnel name even when the historical payload has a readable name', async () => {
    await i18n.changeLanguage('en-US');
    const markup = render(item({
      severity: 'info',
      category: 'tunnel',
      action: 'updated',
      payload: {
        summary_key: 'activity.tunnel.updated',
        summary_args: { tunnel_name: 'Old tunnel name' },
      },
      tunnels: [{ tunnel_id: 'tunnel-id', relation: 'subject', name: 'Current tunnel name' }],
    }));

    expect(markup).toContain('Current tunnel name was updated');
    expect(markup).not.toContain('Old tunnel name');
  });

  test('labels both sides of a tunnel migration with readable names', async () => {
    await i18n.changeLanguage('en-US');
    const markup = render(item({
      severity: 'info',
      category: 'tunnel',
      action: 'migrated',
      actor: { type: 'admin', id: 'admin-id', name: 'alice' },
      payload: {
        summary_key: 'activity.tunnel.migrated',
        summary_args: { tunnel_name: 'Remote desktop', before: 'old-client-id', after: 'new-client-id' },
      },
      clients: [
        { client_id: 'new-client-id', relation: 'target', display_name: 'Office Mac' },
        { client_id: 'old-client-id', relation: 'related', display_name: 'Home Mac' },
      ],
      tunnels: [{ tunnel_id: 'tunnel-id', relation: 'subject', name: 'Remote desktop' }],
    }));

    expect(markup).toContain('Remote desktop target client was migrated');
    expect(markup).toContain('Target client');
    expect(markup).toContain('Office Mac');
    expect(markup).toContain('Previous target');
    expect(markup).toContain('Home Mac');
    expect(markup).not.toContain('old-client-id');
    expect(markup).not.toContain('new-client-id');
    expect(markup).not.toContain('tunnel-id');
  });

  test('uses current subject names when the activity is rendered in Chinese', async () => {
    await i18n.changeLanguage('zh-CN');
    const markup = render(item({
      severity: 'info',
      category: 'client',
      action: 'online',
      payload: {
        summary_key: 'activity.client.online',
        summary_args: { client_name: '旧设备名' },
      },
      clients: [{ client_id: 'client-id', relation: 'subject', display_name: '现设备备注' }],
    }));

    expect(markup).toContain('现设备备注 已上线');
    expect(markup).not.toContain('旧设备名');
    await i18n.changeLanguage('en-US');
  });

  test('labels client, tunnel, and P2P diagnostics by affected link', async () => {
    await i18n.changeLanguage('zh-CN');
    const clientMarkup = render(item({
      category: 'client',
      action: 'offline',
      payload: { summary_key: 'activity.client.offline', reason_code: 'timeout' },
    }));
    expect(clientMarkup).toContain('离线原因：');
    expect(clientMarkup).toContain('等待数据通道建立超时。');
    const tunnelMarkup = render(item({
      category: 'tunnel',
      action: 'runtime_error',
      payload: { summary_key: 'activity.tunnel.runtime_error', reason_code: 'reconcile_failed' },
    }));
    expect(tunnelMarkup).toContain('原因：');
    expect(tunnelMarkup).toContain('隧道配置下发失败。');
    const p2pMarkup = render(item({
      category: 'p2p',
      action: 'session_closed',
      payload: { summary_key: 'activity.p2p.session_closed', reason_code: 'participant_offline' },
    }));
    expect(p2pMarkup).toContain('原因：');
    expect(p2pMarkup).toContain('一名 P2P 参与客户端已离线。');
    await i18n.changeLanguage('en-US');
  });

  test('inlines the subject tunnel name for P2P attach events without payload names', async () => {
    await i18n.changeLanguage('zh-CN');
    const markup = render(item({
      severity: 'debug',
      category: 'p2p',
      action: 'tunnel_attached',
      actor: { type: 'system' },
      payload: { summary_key: 'activity.p2p.tunnel_attached' },
      tunnels: [{ tunnel_id: 'tunnel-id', relation: 'subject', name: '专线' }],
    }));

    expect(markup).toContain('专线 加入了 P2P 会话');
    expect(markup).not.toContain('隧道：');
    await i18n.changeLanguage('en-US');
  });

  test('hides noise: unattributable offline reasons and placeholder actors', async () => {
    await i18n.changeLanguage('zh-CN');
    for (const code of ['unknown', 'data_channel_closed', 'transport_error']) {
      const markup = render(item({
        severity: 'info',
        category: 'client',
        action: 'offline',
        actor: { type: 'system' },
        payload: {
          summary_key: 'activity.client.offline',
          summary_args: { client_name: '办公室主机' },
          reason_code: code,
        },
        clients: [{ client_id: 'client-1', relation: 'subject', display_name: '办公室主机' }],
      }));
      expect(markup).toContain('办公室主机 已离线');
      expect(markup).not.toContain('原因');
      expect(markup).not.toContain('操作方');
    }
    await i18n.changeLanguage('en-US');
  });
});
