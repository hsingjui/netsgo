package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWebhookCatalogLocalizesHumanVariablesAndUsesGenericFixtures(t *testing.T) {
	catalog := activityWebhookCatalog()
	online := catalog.Fixtures["client.online"]
	if online["event.name.zh-CN"] != "客户端上线" || online["event.name.en-US"] != "Client online" {
		t.Fatalf("localized online names = (%#v, %#v)", online["event.name.zh-CN"], online["event.name.en-US"])
	}
	if online["event.summary.zh-CN"] != "Test client node 已上线" || online["event.summary.en-US"] != "Test client node came online" {
		t.Fatalf("localized online summaries = (%#v, %#v)", online["event.summary.zh-CN"], online["event.summary.en-US"])
	}
	if online["client.name"] != "Test client node" {
		t.Fatalf("sample client name = %#v", online["client.name"])
	}
	if len(catalog.Locales) != len(supportedWebhookLocales()) {
		t.Fatalf("catalog locales = %#v", catalog.Locales)
	}

	raw, err := json.Marshal(catalog.Fixtures)
	if err != nil {
		t.Fatal(err)
	}
	for _, specific := range []string{"香港节点", "深圳渲染节点", "CRM HTTPS", "hk-edge"} {
		if strings.Contains(string(raw), specific) {
			t.Fatalf("Webhook fixtures should not contain environment-specific sample %q", specific)
		}
	}
}

func TestWebhookCatalogClientVariablesCoverClientAndTunnelEvents(t *testing.T) {
	catalog := activityWebhookCatalog()
	variables := make(map[string]WebhookVariable, len(catalog.Variables))
	for _, variable := range catalog.Variables {
		variables[variable.Key] = variable
	}

	for _, key := range []string{"event.id", "event.type", "webhook.id", "client.hostname"} {
		if _, ok := variables[key]; ok {
			t.Fatalf("removed template variable %q is still exposed", key)
		}
		if strings.Contains(catalog.DefaultBody, "{{"+key+"}}") {
			t.Fatalf("default Webhook body still references %q", key)
		}
	}

	for _, key := range []string{"client.id", "client.name"} {
		variable, ok := variables[key]
		if !ok {
			t.Fatalf("client template variable %q is missing", key)
		}
		if !webhookVariableSupportsEvents(variable, []string{"client.online"}) {
			t.Fatalf("%q should be available for client events", key)
		}
		if !webhookVariableSupportsEvents(variable, []string{"tunnel.runtime_error"}) {
			t.Fatalf("%q should be available for tunnel events", key)
		}
		if webhookVariableSupportsEvents(variable, []string{"p2p.failed"}) {
			t.Fatalf("%q must not imply one client for a multi-peer P2P event", key)
		}
	}

	tunnel := catalog.Fixtures["tunnel.runtime_error"]
	if tunnel["client.id"] != "client_test_node" || tunnel["client.name"] != "Test client node" {
		t.Fatalf("sample tunnel owner client = (%#v, %#v)", tunnel["client.id"], tunnel["client.name"])
	}
	if _, ok := catalog.Fixtures["p2p.failed"]["client.id"]; ok {
		t.Fatal("P2P fixture exposes an ambiguous singular client")
	}
}

func TestWebhookTunnelValuesUseOwnerClient(t *testing.T) {
	snapshot := webhookEventSnapshot{
		ID:   "event-1",
		Type: "tunnel.runtime_error",
		Clients: []webhookClientSnapshot{
			{ID: "client-ingress", Name: "Ingress", Relation: "ingress"},
			{ID: "client-owner", Name: "Renamed owner", Relation: "owner"},
			{ID: "client-owner", Name: "Renamed owner", Relation: "target"},
		},
		Tunnels: []webhookTunnelSnapshot{{ID: "tunnel-1", Name: "Tunnel"}},
	}

	values := snapshot.values("delivery-1", "webhook-1", "Webhook")
	if values["client.id"] != "client-owner" || values["client.name"] != "Renamed owner" {
		t.Fatalf("tunnel owner client values = (%#v, %#v)", values["client.id"], values["client.name"])
	}
	if _, ok := values["client.hostname"]; ok {
		t.Fatal("client.hostname should not be exposed as a template value")
	}
}

func TestWebhookLocalizedCatalogCoversEvents(t *testing.T) {
	catalog := activityWebhookCatalog()
	for _, event := range catalog.Events {
		values := catalog.Fixtures[event.Key]
		for _, locale := range supportedWebhookLocales() {
			if values["event.name."+string(locale)] == "" || values["event.summary."+string(locale)] == "" {
				t.Fatalf("event %q is missing %q human text", event.Key, locale)
			}
		}
	}
}

func TestWebhookEventSummaryPreservesRealSubjectName(t *testing.T) {
	snapshot := webhookEventSnapshot{
		Type:    "client.online",
		Clients: []webhookClientSnapshot{{ID: "client-real", Name: "自定义节点名称"}},
	}
	values := snapshot.values("dlv", "wh", "Webhook")
	if values["event.summary.zh-CN"] != "自定义节点名称 已上线" {
		t.Fatalf("Chinese summary = %#v", values["event.summary.zh-CN"])
	}
	if values["event.summary.en-US"] != "自定义节点名称 came online" {
		t.Fatalf("English summary = %#v", values["event.summary.en-US"])
	}
}
