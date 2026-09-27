package server

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRenderWebhookRequestPreservesTypedJSONAndRequestMetadata(t *testing.T) {
	config := webhookConfigSnapshot{
		ID: "wh_render", Revision: 7, Name: "Render Webhook", Method: WebhookMethodPOST,
		URL: "https://example.test/hook?text={{text}}&event={{event.type}}",
		Headers: []WebhookHeader{
			{Key: "X-Plain", Value: "prefix {{text}}"},
			{Key: "", Value: "ignored"},
		},
		Body: `{"number":"{{number}}","boolean":"{{boolean}}","array":"{{array}}","object":"{{object}}","nil":"{{nil}}","embedded":"n={{number}}","html":"{{html}}","missing":"{{missing}}"}`,
	}
	values := map[string]any{
		"delivery.id": "dlv-render", "event.id": "evt-render", "event.type": "client.online",
		"text": "A B/深圳?x=1&y=2", "number": json.Number("12.50"), "boolean": true,
		"array": []any{"a", float64(2)}, "object": map[string]any{"ok": true}, "nil": nil, "html": "<tag>&",
	}

	rendered, err := renderWebhookRequest(config, values, 2)
	if err != nil {
		t.Fatal(err)
	}
	if rendered.Method != WebhookMethodPOST || rendered.URL != "https://example.test/hook?text=A%20B%2F%E6%B7%B1%E5%9C%B3%3Fx%3D1%26y%3D2&event=client.online" {
		t.Fatalf("rendered request method=%q URL=%q", rendered.Method, rendered.URL)
	}
	wantHeaders := map[string]string{
		"X-Plain": "prefix A B/深圳?x=1&y=2", "User-Agent": "NetsGo-Webhook/1",
		"X-NetsGo-Delivery": "dlv-render", "X-NetsGo-Event": "evt-render", "X-NetsGo-Attempt": "2",
	}
	if !reflect.DeepEqual(rendered.Headers, wantHeaders) {
		t.Fatalf("rendered headers = %#v, want %#v", rendered.Headers, wantHeaders)
	}
	if rendered.Body == nil {
		t.Fatal("POST body is nil")
	}
	var body map[string]any
	decoder := json.NewDecoder(strings.NewReader(*rendered.Body))
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil {
		t.Fatalf("decode rendered body: %v\n%s", err, *rendered.Body)
	}
	if body["number"] != json.Number("12.50") || body["boolean"] != true || body["nil"] != nil || body["embedded"] != "n=12.50" {
		t.Fatalf("typed body scalars = %#v", body)
	}
	if !reflect.DeepEqual(body["array"], []any{"a", json.Number("2")}) || !reflect.DeepEqual(body["object"], map[string]any{"ok": true}) {
		t.Fatalf("typed body composites = array %#v object %#v", body["array"], body["object"])
	}
	if body["html"] != "<tag>&" || body["missing"] != "{{missing}}" || strings.Contains(*rendered.Body, `\u003c`) {
		t.Fatalf("rendered body escaping/unknown token = %s", *rendered.Body)
	}
}

func TestRenderWebhookRequestGETHasNoBodyAndRefreshesAttempt(t *testing.T) {
	config := webhookConfigSnapshot{
		ID: "wh_get", Name: "GET Webhook", Method: WebhookMethodGET,
		URL: "https://example.test/{{delivery.id}}", Body: "not-json",
	}
	values := map[string]any{"delivery.id": "dlv-get", "event.id": "evt-get"}
	first, err := renderWebhookRequest(config, values, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderWebhookRequest(config, values, 3)
	if err != nil {
		t.Fatal(err)
	}
	if first.Body != nil || second.Body != nil || first.Headers["X-NetsGo-Attempt"] != "1" || second.Headers["X-NetsGo-Attempt"] != "3" {
		t.Fatalf("GET render first=%+v second=%+v", first, second)
	}
}

func TestRenderWebhookRequestRejectsOversizedRenderedRequest(t *testing.T) {
	config := webhookConfigSnapshot{
		ID: "wh_large", Name: "Large", Method: WebhookMethodGET,
		URL: "https://example.test/" + strings.Repeat("x", webhookRenderedRequestMax),
	}
	_, err := renderWebhookRequest(config, map[string]any{"delivery.id": "dlv", "event.id": "evt"}, 1)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized render error = %v", err)
	}
}

func TestWebhookTemplateIssuesCoverUnknownSurfaceAndEventAvailability(t *testing.T) {
	variables := []WebhookVariable{
		{Key: "all", Surfaces: []string{"url", "body"}, AvailableForEvents: "all"},
		{Key: "client", Surfaces: []string{"url"}, AvailableForEvents: []string{"client.online"}},
		{Key: "p2p", Surfaces: []string{"body"}, AvailableForEvents: []any{"p2p.connected"}},
		{Key: "invalid", Surfaces: []string{"url"}, AvailableForEvents: 42},
	}
	tests := []struct {
		name, value, surface, field, code string
		events                            []string
	}{
		{name: "unknown", value: "{{ missing }}", surface: "url", events: []string{"client.online"}, field: "url", code: "unknown_variable"},
		{name: "unsupported surface", value: "{{client}}", surface: "body", events: []string{"client.online"}, field: "body", code: "unsupported_surface"},
		{name: "missing one selected event", value: "{{client}}", surface: "url", events: []string{"client.online", "client.offline"}, field: "url", code: "unavailable_variable"},
		{name: "empty events", value: "{{client}}", surface: "url", field: "url", code: "unavailable_variable"},
		{name: "invalid availability type", value: "{{invalid}}", surface: "url", events: []string{"client.online"}, field: "url", code: "unavailable_variable"},
		{name: "header field mapping", value: "{{p2p}}", surface: "header", events: []string{"p2p.connected"}, field: "headers", code: "unsupported_surface"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issues := webhookTemplateIssues(test.value, test.events, test.surface, variables)
			if len(issues) != 1 {
				t.Fatalf("issues = %v, want one", issues)
			}
			assertWebhookValidationError(t, issues[0], test.field, test.code)
		})
	}
	if issues := webhookTemplateIssues("{{all}} {{client}} {{p2p}}", []string{"p2p.connected"}, "body", variables); len(issues) != 1 {
		t.Fatalf("supported all/p2p variables issues = %v", issues)
	}
}

func TestEncodeURIComponentMatchesWebhookTemplateContract(t *testing.T) {
	tests := map[string]string{
		"AZaz09-_.!~*'()": "AZaz09-_.!~*'()",
		"a b/c?d=e&f":     "a%20b%2Fc%3Fd%3De%26f",
		"深圳":              "%E6%B7%B1%E5%9C%B3",
		"%":               "%25",
	}
	for input, want := range tests {
		if got := encodeURIComponent(input); got != want {
			t.Fatalf("encodeURIComponent(%q) = %q, want %q", input, got, want)
		}
	}
}
