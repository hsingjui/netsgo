package server

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func cloneWebhookInput(input WebhookConfigInput) WebhookConfigInput {
	input.TargetIDs = append([]string(nil), input.TargetIDs...)
	input.Headers = append([]WebhookHeader(nil), input.Headers...)
	input.Events = append([]string(nil), input.Events...)
	return input
}

func assertWebhookValidationError(t *testing.T, err error, field, code string) {
	t.Helper()
	var validation *webhookValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %v, want webhookValidationError", err)
	}
	if validation.Field != field || validation.Code != code {
		t.Fatalf("validation error = field %q code %q, want field %q code %q", validation.Field, validation.Code, field, code)
	}
}

func TestNormalizeWebhookInputTrimsAndDeduplicates(t *testing.T) {
	input := testWebhookInput("  wh_normalized  ")
	input.Name = "  Normalized Webhook  "
	input.URL = "  https://example.test/hook  "
	input.TargetMode = WebhookTargetSelected
	input.TargetIDs = []string{" client-b ", "", "client-a", "client-b"}
	input.Events = []string{" client.offline ", "client.online", "client.offline", ""}
	input.Headers = []WebhookHeader{{ID: " content ", Key: " Content-Type ", Value: " application/json "}}

	normalized := normalizeWebhookInput(input)
	if normalized.ID != "wh_normalized" || normalized.Name != "Normalized Webhook" || normalized.URL != "https://example.test/hook" {
		t.Fatalf("normalized scalar fields = %#v", normalized)
	}
	if got := strings.Join(normalized.TargetIDs, ","); got != "client-b,client-a" {
		t.Fatalf("normalized targets = %q", got)
	}
	if got := strings.Join(normalized.Events, ","); got != "client.offline,client.online" {
		t.Fatalf("normalized events = %q", got)
	}
	if normalized.Headers[0].ID != "content" || normalized.Headers[0].Key != "Content-Type" || normalized.Headers[0].Value != " application/json " {
		t.Fatalf("normalized header = %#v", normalized.Headers[0])
	}

	input.TargetMode = WebhookTargetAll
	input.TargetIDs = []string{"client-a"}
	input.Headers = nil
	normalized = normalizeWebhookInput(input)
	if len(normalized.TargetIDs) != 0 || normalized.Headers == nil || len(normalized.Headers) != 0 {
		t.Fatalf("all-target normalization = targets %#v headers %#v", normalized.TargetIDs, normalized.Headers)
	}
}

func TestValidateWebhookInputAcceptsSupportedGETAndPOSTConfigurations(t *testing.T) {
	catalog := activityWebhookCatalog()
	post := normalizeWebhookInput(testWebhookInput("wh_valid_post"))
	if err := validateWebhookInput(post, catalog.Fixtures, catalog.Variables, true); err != nil {
		t.Fatalf("valid POST configuration: %v", err)
	}

	get := cloneWebhookInput(post)
	get.ID = "wh_valid_get"
	get.Method = WebhookMethodGET
	get.URL = "https://example.test/hook?event={{event.name.en-US}}&client={{client.id}}"
	get.Body = "not-json-is-ignored-for-get"
	if err := validateWebhookInput(get, catalog.Fixtures, catalog.Variables, true); err != nil {
		t.Fatalf("valid GET configuration: %v", err)
	}
}

func TestValidateWebhookInputRejectsInvalidConfigurationMatrix(t *testing.T) {
	catalog := activityWebhookCatalog()
	base := normalizeWebhookInput(testWebhookInput("wh_validation"))

	tests := []struct {
		name   string
		field  string
		code   string
		mutate func(*WebhookConfigInput)
	}{
		{name: "invalid id", field: "id", code: "invalid_id", mutate: func(input *WebhookConfigInput) { input.ID = "_invalid" }},
		{name: "id too long", field: "id", code: "invalid_id", mutate: func(input *WebhookConfigInput) { input.ID = strings.Repeat("a", 129) }},
		{name: "missing name", field: "name", code: "required", mutate: func(input *WebhookConfigInput) { input.Name = "" }},
		{name: "name too long by rune", field: "name", code: "too_long", mutate: func(input *WebhookConfigInput) { input.Name = strings.Repeat("界", 121) }},
		{name: "invalid target kind", field: "target_kind", code: "invalid_target_kind", mutate: func(input *WebhookConfigInput) { input.TargetKind = "server" }},
		{name: "invalid target mode", field: "target_mode", code: "invalid_target_mode", mutate: func(input *WebhookConfigInput) { input.TargetMode = "some" }},
		{name: "selected target required", field: "targets", code: "required", mutate: func(input *WebhookConfigInput) { input.TargetMode = WebhookTargetSelected; input.TargetIDs = nil }},
		{name: "too many targets", field: "targets", code: "too_many", mutate: func(input *WebhookConfigInput) {
			input.TargetMode = WebhookTargetSelected
			input.TargetIDs = make([]string, webhookMaxTargets+1)
			for index := range input.TargetIDs {
				input.TargetIDs[index] = "client-" + strconv.Itoa(index)
			}
		}},
		{name: "invalid target encoding", field: "targets", code: "invalid_target", mutate: func(input *WebhookConfigInput) {
			input.TargetMode = WebhookTargetSelected
			input.TargetIDs = []string{string([]byte{0xff})}
		}},
		{name: "missing events", field: "events", code: "required", mutate: func(input *WebhookConfigInput) { input.Events = nil }},
		{name: "unknown event", field: "events", code: "unknown_event", mutate: func(input *WebhookConfigInput) { input.Events = []string{"client.unknown"} }},
		{name: "event target mismatch", field: "events", code: "event_target_mismatch", mutate: func(input *WebhookConfigInput) { input.Events = []string{"tunnel.stopped"} }},
		{name: "invalid method", field: "method", code: "invalid_method", mutate: func(input *WebhookConfigInput) { input.Method = "PATCH" }},
		{name: "missing url", field: "url", code: "required", mutate: func(input *WebhookConfigInput) { input.URL = "" }},
		{name: "url too long", field: "url", code: "too_long", mutate: func(input *WebhookConfigInput) { input.URL = "https://example.test/" + strings.Repeat("a", 8<<10) }},
		{name: "unknown url variable", field: "url", code: "unknown_variable", mutate: func(input *WebhookConfigInput) { input.URL = "https://example.test/{{missing.value}}" }},
		{name: "unsupported url variable", field: "url", code: "unsupported_surface", mutate: func(input *WebhookConfigInput) { input.URL = "https://example.test/{{delivery.attempt}}" }},
		{name: "invalid rendered url", field: "url", code: "invalid_url", mutate: func(input *WebhookConfigInput) { input.URL = "not-a-url" }},
		{name: "invalid url scheme", field: "url", code: "invalid_url_scheme", mutate: func(input *WebhookConfigInput) { input.URL = "ftp://example.test/hook" }},
		{name: "too many headers", field: "headers", code: "too_many", mutate: func(input *WebhookConfigInput) {
			input.Headers = make([]WebhookHeader, webhookMaxHeaders+1)
			for index := range input.Headers {
				input.Headers[index] = WebhookHeader{Key: "X-Test-" + string(rune('A'+index%26)), Value: "value"}
			}
		}},
		{name: "invalid header name", field: "headers", code: "invalid_header", mutate: func(input *WebhookConfigInput) { input.Headers = []WebhookHeader{{Key: "Bad Header", Value: "value"}} }},
		{name: "header newline", field: "headers", code: "invalid_header", mutate: func(input *WebhookConfigInput) { input.Headers = []WebhookHeader{{Key: "X-Test", Value: "one\r\ntwo"}} }},
		{name: "duplicate header case insensitive", field: "headers", code: "duplicate_header", mutate: func(input *WebhookConfigInput) {
			input.Headers = []WebhookHeader{{Key: "X-Test", Value: "one"}, {Key: "x-test", Value: "two"}}
		}},
		{name: "restricted header", field: "headers", code: "restricted_header", mutate: func(input *WebhookConfigInput) {
			input.Headers = []WebhookHeader{{Key: "X-NetsGo-Delivery", Value: "manual"}}
		}},
		{name: "header too long", field: "headers", code: "too_long", mutate: func(input *WebhookConfigInput) {
			input.Headers = []WebhookHeader{{Key: "X-Test", Value: strings.Repeat("a", (8<<10)+1)}}
		}},
		{name: "unknown header variable", field: "headers", code: "unknown_variable", mutate: func(input *WebhookConfigInput) {
			input.Headers = []WebhookHeader{{Key: "X-Test", Value: "{{missing.value}}"}}
		}},
		{name: "unsupported header variable", field: "headers", code: "unsupported_surface", mutate: func(input *WebhookConfigInput) {
			input.Headers = []WebhookHeader{{Key: "X-Test", Value: "{{delivery.attempt}}"}}
		}},
		{name: "unavailable header variable", field: "headers", code: "unavailable_variable", mutate: func(input *WebhookConfigInput) {
			input.Headers = []WebhookHeader{{Key: "X-Test", Value: "{{tunnel.id}}"}}
		}},
		{name: "body too long", field: "body", code: "too_long", mutate: func(input *WebhookConfigInput) { input.Body = strings.Repeat(" ", webhookTemplateMaxBytes+1) }},
		{name: "invalid json body", field: "body", code: "invalid_json", mutate: func(input *WebhookConfigInput) { input.Body = "{" }},
		{name: "null body", field: "body", code: "body_must_be_object", mutate: func(input *WebhookConfigInput) { input.Body = "null" }},
		{name: "array body", field: "body", code: "body_must_be_object", mutate: func(input *WebhookConfigInput) { input.Body = "[]" }},
		{name: "unavailable body variable", field: "body", code: "unavailable_variable", mutate: func(input *WebhookConfigInput) { input.Body = `{"tunnel":"{{tunnel.id}}"}` }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := cloneWebhookInput(base)
			test.mutate(&input)
			err := validateWebhookInput(normalizeWebhookInput(input), catalog.Fixtures, catalog.Variables, true)
			assertWebhookValidationError(t, err, test.field, test.code)
		})
	}
}

func TestValidateWebhookInputRejectsMissingFixtureAndRenderedSize(t *testing.T) {
	catalog := activityWebhookCatalog()
	base := normalizeWebhookInput(testWebhookInput("wh_fixture_validation"))

	fixtures := make(map[string]map[string]any, len(catalog.Fixtures))
	for key, value := range catalog.Fixtures {
		fixtures[key] = value
	}
	delete(fixtures, "client.online")
	assertWebhookValidationError(t, validateWebhookInput(base, fixtures, catalog.Variables, true), "events", "missing_fixture")

	fixtures = make(map[string]map[string]any, len(catalog.Fixtures))
	for key, value := range catalog.Fixtures {
		fixtures[key] = cloneWebhookValues(value)
	}
	fixtures["client.online"]["event.summary.en-US"] = strings.Repeat("x", webhookRenderedRequestMax)
	assertWebhookValidationError(t, validateWebhookInput(base, fixtures, catalog.Variables, true), "body", "invalid_template")
}
