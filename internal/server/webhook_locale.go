package server

import "fmt"

type WebhookLocale string

const (
	WebhookLocaleEnglish WebhookLocale = "en-US"
	WebhookLocaleChinese WebhookLocale = "zh-CN"
)

type webhookLocalizedEvent struct {
	Name    string
	Summary string
}

var webhookEventNames = map[WebhookLocale]map[string]string{
	WebhookLocaleEnglish: {
		"client.online":            "Client online",
		"client.offline":           "Client offline",
		"tunnel.stopped":           "Tunnel stopped",
		"tunnel.resumed":           "Tunnel re-enabled",
		"tunnel.runtime_changed":   "Tunnel runtime state changed",
		"tunnel.runtime_error":     "Tunnel runtime error",
		"tunnel.runtime_recovered": "Tunnel runtime recovered",
		"p2p.checking":             "P2P checking",
		"p2p.connected":            "P2P connected",
		"p2p.failed":               "P2P connection failed",
		"p2p.fallback":             "P2P fell back to Server relay",
		"p2p.session_closed":       "P2P session closed",
	},
	WebhookLocaleChinese: {
		"client.online":            "客户端上线",
		"client.offline":           "客户端离线",
		"tunnel.stopped":           "隧道已停止",
		"tunnel.resumed":           "隧道已重新启用",
		"tunnel.runtime_changed":   "隧道运行状态改变",
		"tunnel.runtime_error":     "隧道运行异常",
		"tunnel.runtime_recovered": "隧道已从异常中恢复",
		"p2p.checking":             "P2P 检查中",
		"p2p.connected":            "P2P 已直连",
		"p2p.failed":               "P2P 直连失败",
		"p2p.fallback":             "回退到 Server 中继",
		"p2p.session_closed":       "P2P 会话关闭",
	},
}

var webhookEventSummaryFormats = map[WebhookLocale]map[string]string{
	WebhookLocaleEnglish: {
		"client.online":            "%s came online",
		"client.offline":           "%s went offline",
		"tunnel.stopped":           "%s was stopped",
		"tunnel.resumed":           "%s was re-enabled",
		"tunnel.runtime_changed":   "%s runtime state changed",
		"tunnel.runtime_error":     "%s encountered a runtime error",
		"tunnel.runtime_recovered": "%s recovered from a runtime error",
	},
	WebhookLocaleChinese: {
		"client.online":            "%s 已上线",
		"client.offline":           "%s 已离线",
		"tunnel.stopped":           "%s 已停止",
		"tunnel.resumed":           "%s 已重新启用",
		"tunnel.runtime_changed":   "%s 运行状态已改变",
		"tunnel.runtime_error":     "%s 运行异常",
		"tunnel.runtime_recovered": "%s 已从运行异常中恢复",
	},
}

func supportedWebhookLocales() []WebhookLocale {
	return []WebhookLocale{WebhookLocaleEnglish, WebhookLocaleChinese}
}

// localizeWebhookEvent renders human-readable text for one locale. The locale
// must come from supportedWebhookLocales().
func localizeWebhookEvent(snapshot webhookEventSnapshot, locale WebhookLocale) webhookLocalizedEvent {
	name := webhookEventNames[locale][snapshot.Type]
	if name == "" {
		name = snapshot.Type
	}
	summary := name
	if format := webhookEventSummaryFormats[locale][snapshot.Type]; format != "" {
		subjectName := ""
		if len(snapshot.Clients) == 1 {
			subjectName = snapshot.Clients[0].Name
		} else if len(snapshot.Tunnels) == 1 {
			subjectName = snapshot.Tunnels[0].Name
		}
		if subjectName != "" {
			summary = fmt.Sprintf(format, subjectName)
		}
	}
	return webhookLocalizedEvent{Name: name, Summary: summary}
}
