import { expect, test } from './fixtures';
import {
  createClientToClientTunnel,
  deleteTunnelByName,
  e2eConfig,
  expectHTTPUnavailable,
  expectHTTPContains,
  expectUDPUnavailable,
  login,
  sendUDP,
  uniqueTunnelName,
  waitForClientPair,
  waitForTunnelState,
} from './helpers';

test('creates TCP and UDP client-to-client tunnels from the web UI @smoke', async ({ page }) => {
  const tcpTunnelName = uniqueTunnelName('playwright-c2c-tcp');
  const udpTunnelName = uniqueTunnelName('playwright-c2c-udp');

  try {
    await login(page);
    const { source, ingress } = await waitForClientPair(page);

    await createClientToClientTunnel(page, {
      sourceClientID: source.id,
      sourceClientName: source.info.hostname,
      ingressClientID: ingress.id,
      ingressClientName: ingress.info.hostname,
      name: tcpTunnelName,
      protocol: 'TCP',
      targetHost: 'tcp-backend',
      targetPort: '18083',
      ingressBindIP: '0.0.0.0',
      ingressPort: '18090',
    });
    await waitForTunnelState(page, tcpTunnelName, 'active');
    await expectHTTPContains(page, e2eConfig.tcpIngressHostPort, 'playwright tcp c2c response');

    await createClientToClientTunnel(page, {
      sourceClientID: source.id,
      sourceClientName: source.info.hostname,
      ingressClientID: ingress.id,
      ingressClientName: ingress.info.hostname,
      name: udpTunnelName,
      protocol: 'UDP',
      targetHost: 'udp-backend',
      targetPort: '18084',
      ingressBindIP: '0.0.0.0',
      ingressPort: '18091',
    });
    await waitForTunnelState(page, udpTunnelName, 'active');

    const udpResponse = await sendUDP('127.0.0.1', e2eConfig.udpIngressHostPort, 'hello');
    expect(udpResponse).toContain('playwright-udp-c2c-response');
  } finally {
    await deleteTunnelByName(page, udpTunnelName);
    await deleteTunnelByName(page, tcpTunnelName);
    await expectUDPUnavailable('127.0.0.1', e2eConfig.udpIngressHostPort);
    await expectHTTPUnavailable(page, e2eConfig.tcpIngressHostPort);
  }
});
