package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type WebhookVariable struct {
	Key                string   `json:"key"`
	Group              string   `json:"group"`
	ValueType          string   `json:"value_type"`
	Surfaces           []string `json:"surfaces"`
	AvailableForEvents any      `json:"available_for_events"`
	Optional           bool     `json:"optional,omitempty"`
}

type renderedWebhookRequest struct {
	Method  WebhookMethod
	URL     string
	Headers map[string]string
	Body    *string
}

func (r renderedWebhookRequest) size() int {
	size := len(r.Method) + len(r.URL)
	for key, value := range r.Headers {
		size += len(key) + len(value)
	}
	if r.Body != nil {
		size += len(*r.Body)
	}
	return size
}

var (
	webhookTokenPattern      = regexp.MustCompile(`{{\s*([^}]+?)\s*}}`)
	webhookExactTokenPattern = regexp.MustCompile(`^{{\s*([^}]+?)\s*}}$`)
)

func webhookTemplateIssues(value string, events []string, surface string, variables []WebhookVariable) []error {
	byKey := make(map[string]WebhookVariable, len(variables))
	for _, variable := range variables {
		byKey[variable.Key] = variable
	}
	var issues []error
	for _, match := range webhookTokenPattern.FindAllStringSubmatch(value, -1) {
		key := strings.TrimSpace(match[1])
		variable, ok := byKey[key]
		if !ok {
			issues = append(issues, invalidWebhook(surfaceField(surface), "unknown_variable", fmt.Sprintf("unknown Webhook variable %q", key)))
			continue
		}
		if !slicesContains(variable.Surfaces, surface) {
			issues = append(issues, invalidWebhook(surfaceField(surface), "unsupported_surface", fmt.Sprintf("Webhook variable %q is unavailable here", key)))
			continue
		}
		if !webhookVariableSupportsEvents(variable, events) {
			issues = append(issues, invalidWebhook(surfaceField(surface), "unavailable_variable", fmt.Sprintf("Webhook variable %q is unavailable for the selected events", key)))
		}
	}
	return issues
}

func surfaceField(surface string) string {
	if surface == "header" {
		return "headers"
	}
	return surface
}

func slicesContains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func webhookVariableSupportsEvents(variable WebhookVariable, events []string) bool {
	if raw, ok := variable.AvailableForEvents.(string); ok {
		return raw == "all"
	}
	available := make(map[string]struct{})
	switch raw := variable.AvailableForEvents.(type) {
	case []string:
		for _, event := range raw {
			available[event] = struct{}{}
		}
	case []any:
		for _, event := range raw {
			if value, ok := event.(string); ok {
				available[value] = struct{}{}
			}
		}
	default:
		return false
	}
	if len(events) == 0 {
		return false
	}
	for _, event := range events {
		if _, ok := available[event]; !ok {
			return false
		}
	}
	return true
}

func cloneWebhookValues(values map[string]any) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func renderWebhookRequest(config webhookConfigSnapshot, baseValues map[string]any, attempt int) (renderedWebhookRequest, error) {
	values := cloneWebhookValues(baseValues)
	values["delivery.attempt"] = attempt
	values["webhook.id"] = config.ID
	values["webhook.name"] = config.Name

	request := renderedWebhookRequest{
		Method:  config.Method,
		URL:     renderWebhookText(config.URL, values, true),
		Headers: make(map[string]string, len(config.Headers)+4),
	}
	for _, header := range config.Headers {
		if strings.TrimSpace(header.Key) == "" {
			continue
		}
		request.Headers[strings.TrimSpace(header.Key)] = renderWebhookText(header.Value, values, false)
	}
	request.Headers["User-Agent"] = "NetsGo-Webhook/1"
	request.Headers["X-NetsGo-Delivery"] = webhookValueText(values["delivery.id"])
	request.Headers["X-NetsGo-Event"] = webhookValueText(values["event.id"])
	request.Headers["X-NetsGo-Attempt"] = strconv.Itoa(attempt)

	if config.Method == WebhookMethodPOST {
		body, err := renderWebhookJSON(config.Body, values)
		if err != nil {
			return renderedWebhookRequest{}, err
		}
		request.Body = &body
	}
	if request.size() > webhookRenderedRequestMax {
		return renderedWebhookRequest{}, fmt.Errorf("rendered Webhook request exceeds %d bytes", webhookRenderedRequestMax)
	}
	return request, nil
}

func renderWebhookText(value string, values map[string]any, encode bool) string {
	return webhookTokenPattern.ReplaceAllStringFunc(value, func(token string) string {
		match := webhookTokenPattern.FindStringSubmatch(token)
		if len(match) != 2 {
			return token
		}
		replacement, ok := values[strings.TrimSpace(match[1])]
		if !ok {
			return token
		}
		text := webhookValueText(replacement)
		if encode {
			return encodeURIComponent(text)
		}
		return text
	})
}

func webhookValueText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprint(value)
		}
		return string(raw)
	}
}

func renderWebhookJSON(body string, values map[string]any) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", fmt.Errorf("decode Webhook JSON template: %w", err)
	}
	value = renderWebhookJSONValue(value, values)
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", fmt.Errorf("encode rendered Webhook JSON: %w", err)
	}
	return strings.TrimSuffix(output.String(), "\n"), nil
}

func renderWebhookJSONValue(value any, values map[string]any) any {
	switch typed := value.(type) {
	case string:
		if match := webhookExactTokenPattern.FindStringSubmatch(typed); len(match) == 2 {
			if replacement, ok := values[strings.TrimSpace(match[1])]; ok {
				return replacement
			}
		}
		return renderWebhookText(typed, values, false)
	case []any:
		for index := range typed {
			typed[index] = renderWebhookJSONValue(typed[index], values)
		}
		return typed
	case map[string]any:
		for key, entry := range typed {
			typed[key] = renderWebhookJSONValue(entry, values)
		}
		return typed
	default:
		return value
	}
}

func encodeURIComponent(value string) string {
	const hex = "0123456789ABCDEF"
	var output strings.Builder
	for _, b := range []byte(value) {
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') ||
			b == '-' || b == '_' || b == '.' || b == '!' || b == '~' || b == '*' || b == '\'' || b == '(' || b == ')' {
			output.WriteByte(b)
			continue
		}
		output.WriteByte('%')
		output.WriteByte(hex[b>>4])
		output.WriteByte(hex[b&15])
	}
	return output.String()
}
