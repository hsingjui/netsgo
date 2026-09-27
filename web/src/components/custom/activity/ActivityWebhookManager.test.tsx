import { describe, expect, test } from 'vitest';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';

import { WebhookDeliveryLog } from './ActivityWebhookManager';
import { i18n } from '@/i18n';
import { TooltipProvider } from '@/components/ui/tooltip';
import type { WebhookInvocation } from '@/types/webhook';

function invocation(overrides: Partial<WebhookInvocation> = {}): WebhookInvocation {
  return {
    id: 'dlv_1',
    webhookId: 'wh_1',
    webhookName: 'webhook wh_1',
    eventId: 'evt_1',
    event: 'client.online',
    occurredAt: '2026-07-23T08:30:00Z',
    status: 'success',
    origin: 'test',
    statusCode: 204,
    durationMs: 350,
    attempts: [],
    requestMethod: 'POST',
    requestUrl: 'https://example.com/hook',
    requestHeaders: {},
    requestBody: null,
    responseHeaders: {},
    responseBody: '',
    createdAt: '2026-07-23T08:30:00Z',
    ...overrides,
  };
}
function renderLog(webhookId: string, invocations: WebhookInvocation[], overrides: Partial<Parameters<typeof WebhookDeliveryLog>[0]> = {}) {
  const client = new QueryClient();
  return renderToStaticMarkup(createElement(
    QueryClientProvider,
    { client },
    createElement(
      TooltipProvider,
      null,
      createElement(WebhookDeliveryLog, {
        webhookId,
        invocations,
        status: 'all',
        onStatusChange: () => undefined,
        hasMore: false,
        loadingMore: false,
        onLoadMore: () => undefined,
        onReplay: () => undefined,
        ...overrides,
      }),
    ),
  ));
}

describe('WebhookDeliveryLog', () => {
  test('renders only the invocations of the selected webhook', async () => {
    await i18n.changeLanguage('en-US');
    const markup = renderLog('wh_1', [
      invocation(),
      invocation({ id: 'dlv_2', webhookId: 'wh_2', event: 'tunnel.stopped', origin: 'event', status: 'failed', statusCode: 500, durationMs: 900 }),
    ]);

    expect(markup).toContain('Client online');
    expect(markup).toContain('client.online');
    expect(markup).toContain('Test call');
    expect(markup).toContain('Delivered');
    expect(markup).not.toContain('Tunnel stopped');
    expect(markup).not.toContain('tunnel.stopped');
    expect(markup).not.toContain('Activity event');
  });

  test('renders em-dash placeholders for missing status code and duration', async () => {
    await i18n.changeLanguage('en-US');
    const markup = renderLog('wh_1', [
      invocation({ statusCode: null, durationMs: null }),
      invocation({ id: 'dlv_2', statusCode: 204, durationMs: 350 }),
    ]);

    expect(markup).toContain('>—<');
    expect(markup).toContain('>204<');
    expect(markup).toContain('350 ms');
  });

  test('renders every localized delivery status badge with its severity variant', async () => {
    await i18n.changeLanguage('en-US');
    const statuses = ['queued', 'retrying', 'success', 'failed', 'canceled'] as const;
    const markup = renderLog('wh_1', statuses.map((status, index) => invocation({ id: `dlv_${index}`, status })));

    expect(markup).toContain('Queued');
    expect(markup).toContain('Retrying');
    expect(markup).toContain('Delivered');
    expect(markup).toContain('Failed');
    expect(markup).toContain('Canceled');
    expect(markup).toContain('data-variant="destructive"');
    expect(markup).toContain('data-variant="secondary"');
    expect(markup.match(/data-variant="outline"/g)?.length).toBeGreaterThanOrEqual(6);
  });
});
