package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) handleAPIAdminWebhookSettings(w http.ResponseWriter, r *http.Request) {
	s.ensureSharedStoreReferences()
	if s.auth == nil || s.auth.adminStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "webhook_settings_unavailable", "Webhook settings are unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		settings, err := s.auth.adminStore.GetWebhookSettings()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "webhook_settings_read_failed", "Failed to read Webhook settings")
			return
		}
		encodeJSON(w, http.StatusOK, settings)
	case http.MethodPut:
		var settings WebhookSettings
		if err := decodeJSONRequestBody(r, &settings); err != nil {
			writeJSONRequestDecodeError(w, err)
			return
		}
		if err := validateWebhookSettings(settings); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_webhook_settings", err.Error())
			return
		}
		previous, prevErr := s.auth.adminStore.GetWebhookSettings()
		activityID, err := s.auth.adminStore.UpdateWebhookSettingsWithActivity(settings, s.activityActorForRequest(r))
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "webhook_settings_update_failed", "Failed to update Webhook settings")
			return
		}
		s.publishActivityID(activityID)
		disabled := 0
		if prevErr == nil && previous.AllowPrivateTargets && !settings.AllowPrivateTargets && s.webhookStore != nil {
			disabled, err = s.webhookStore.DisableWebhooksViolatingPrivatePolicy()
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, "webhook_settings_update_failed", "settings were saved but disabling policy-violating Webhooks failed")
				return
			}
			if disabled > 0 && s.webhookDispatcher != nil {
				s.webhookDispatcher.Wake()
			}
		}
		encodeJSON(w, http.StatusOK, struct {
			WebhookSettings
			DisabledWebhooks int `json:"disabled_webhooks"`
		}{WebhookSettings: settings, DisabledWebhooks: disabled})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method is not allowed")
	}
}

// webhookSettingsLoader keeps the outbound policy live: the store re-reads the
// persisted settings on each use, so admin changes apply without restart.
func (s *Server) webhookSettingsLoader() func() WebhookSettings {
	return func() WebhookSettings {
		if s.auth != nil && s.auth.adminStore != nil {
			if settings, err := s.auth.adminStore.GetWebhookSettings(); err == nil {
				return settings
			}
		}
		return defaultWebhookSettings()
	}
}

type webhookPreviewRequest struct {
	Config WebhookConfigInput `json:"config"`
	Event  string             `json:"event"`
}

func (s *Server) handleAPIWebhookCatalog(w http.ResponseWriter, _ *http.Request) {
	encodeJSON(w, http.StatusOK, activityWebhookCatalog())
}

func (s *Server) handleAPIWebhooks(w http.ResponseWriter, r *http.Request) {
	s.ensureSharedStoreReferences()
	if s.webhookStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "webhook_store_unavailable", "Webhook store is unavailable")
		return
	}
	scope, ok := requireResourceScope(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		items, err := s.webhookStore.List(scope.OwnerUserID)
		if err != nil {
			writeWebhookAPIError(w, err)
			return
		}
		encodeJSON(w, http.StatusOK, items)
		return
	}
	var input WebhookConfigInput
	if err := decodeJSONRequestBodyWithPolicy(r, &input, webhookJSONRequestLimitBytes, false); err != nil {
		writeJSONRequestDecodeError(w, err)
		return
	}
	if strings.TrimSpace(input.ID) == "" {
		input.ID = "wh_" + generateUUID()
	}
	release, err := s.acquireResourceMutation(scope, true)
	if err != nil {
		writeResourceLifecycleError(w, err)
		return
	}
	item, err := s.webhookStore.Create(scope.OwnerUserID, input)
	release()
	if err != nil {
		writeWebhookAPIError(w, err)
		return
	}
	s.publishWebhookChanged(scope.OwnerUserID, "created", item.ID)
	encodeJSON(w, http.StatusCreated, item)
}

func (s *Server) handleAPIWebhookItem(w http.ResponseWriter, r *http.Request) {
	s.ensureSharedStoreReferences()
	if s.webhookStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "webhook_store_unavailable", "Webhook store is unavailable")
		return
	}
	scope, ok := requireResourceScope(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	switch r.Method {
	case http.MethodGet:
		item, err := s.webhookStore.Get(scope.OwnerUserID, id)
		if err != nil {
			writeWebhookAPIError(w, err)
			return
		}
		encodeJSON(w, http.StatusOK, item)
	case http.MethodPut:
		var input WebhookConfigInput
		if err := decodeJSONRequestBodyWithPolicy(r, &input, webhookJSONRequestLimitBytes, false); err != nil {
			writeJSONRequestDecodeError(w, err)
			return
		}
		release, err := s.acquireResourceMutation(scope, true)
		if err != nil {
			writeResourceLifecycleError(w, err)
			return
		}
		item, err := s.webhookStore.Update(scope.OwnerUserID, id, input)
		release()
		if err != nil {
			writeWebhookAPIError(w, err)
			return
		}
		s.publishWebhookChanged(scope.OwnerUserID, "updated", item.ID)
		if s.webhookDispatcher != nil {
			s.webhookDispatcher.Wake()
		}
		encodeJSON(w, http.StatusOK, item)
	case http.MethodDelete:
		release, err := s.acquireResourceMutation(scope, true)
		if err != nil {
			writeResourceLifecycleError(w, err)
			return
		}
		err = s.webhookStore.Delete(scope.OwnerUserID, id)
		release()
		if err != nil {
			writeWebhookAPIError(w, err)
			return
		}
		s.publishWebhookChanged(scope.OwnerUserID, "deleted", id)
		w.WriteHeader(http.StatusNoContent)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) handleAPIWebhookEnabledToggle(w http.ResponseWriter, r *http.Request) {
	s.ensureSharedStoreReferences()
	if s.webhookStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "webhook_store_unavailable", "Webhook store is unavailable")
		return
	}
	scope, ok := requireResourceScope(w, r)
	if !ok {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSONRequestBody(r, &body); err != nil {
		writeJSONRequestDecodeError(w, err)
		return
	}
	release, err := s.acquireResourceMutation(scope, true)
	if err != nil {
		writeResourceLifecycleError(w, err)
		return
	}
	item, err := s.webhookStore.SetEnabled(scope.OwnerUserID, id, body.Enabled)
	release()
	if err != nil {
		writeWebhookAPIError(w, err)
		return
	}
	s.publishWebhookChanged(scope.OwnerUserID, "updated", item.ID)
	if s.webhookDispatcher != nil {
		s.webhookDispatcher.Wake()
	}
	encodeJSON(w, http.StatusOK, item)
}

func (s *Server) handleAPIWebhookPreview(w http.ResponseWriter, r *http.Request) {
	s.ensureSharedStoreReferences()
	if s.webhookStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "webhook_store_unavailable", "Webhook store is unavailable")
		return
	}
	var request webhookPreviewRequest
	if err := decodeJSONRequestBodyWithPolicy(r, &request, webhookJSONRequestLimitBytes, false); err != nil {
		writeJSONRequestDecodeError(w, err)
		return
	}
	preview, err := s.webhookStore.Preview(request.Config, request.Event)
	if err != nil {
		writeWebhookAPIError(w, err)
		return
	}
	encodeJSON(w, http.StatusOK, preview)
}

func (s *Server) handleAPIWebhookTest(w http.ResponseWriter, r *http.Request) {
	s.ensureSharedStoreReferences()
	if s.webhookStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "webhook_store_unavailable", "Webhook store is unavailable")
		return
	}
	scope, ok := requireResourceScope(w, r)
	if !ok {
		return
	}
	var request webhookPreviewRequest
	if err := decodeJSONRequestBodyWithPolicy(r, &request, webhookJSONRequestLimitBytes, false); err != nil {
		writeJSONRequestDecodeError(w, err)
		return
	}
	release, err := s.acquireResourceMutation(scope, true)
	if err != nil {
		writeResourceLifecycleError(w, err)
		return
	}
	delivery, err := s.webhookStore.EnqueueTest(scope.OwnerUserID, request.Config, request.Event)
	release()
	if err != nil {
		writeWebhookAPIError(w, err)
		return
	}
	s.publishWebhookDeliveryChanged(scope.OwnerUserID, delivery)
	if s.webhookDispatcher != nil {
		s.webhookDispatcher.Wake()
	}
	encodeJSON(w, http.StatusAccepted, delivery)
}

func (s *Server) handleAPIWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	s.ensureSharedStoreReferences()
	if s.webhookStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "webhook_store_unavailable", "Webhook store is unavailable")
		return
	}
	scope, ok := requireResourceScope(w, r)
	if !ok {
		return
	}
	webhookID := strings.TrimSpace(r.PathValue("id"))
	// Deliveries of a deleted Webhook stay readable for their retention
	// window, so no live-config prerequisite here: ListDeliveries already
	// scopes strictly to the requesting owner.
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			writeAPIError(w, http.StatusBadRequest, "invalid_delivery_limit", "limit must be between 1 and 100")
			return
		}
		limit = value
	}
	status := WebhookDeliveryStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	if status != "" && status != WebhookDeliveryQueued && status != WebhookDeliveryRetrying && status != WebhookDeliverySuccess && status != WebhookDeliveryFailed && status != WebhookDeliveryCanceled {
		writeAPIError(w, http.StatusBadRequest, "invalid_delivery_status", "delivery status is invalid")
		return
	}
	page, err := s.webhookStore.ListDeliveries(scope.OwnerUserID, webhookID, r.URL.Query().Get("cursor"), limit, status)
	if err != nil {
		writeWebhookAPIError(w, err)
		return
	}
	encodeJSON(w, http.StatusOK, page)
}

func (s *Server) handleAPIWebhookDelivery(w http.ResponseWriter, r *http.Request) {
	s.ensureSharedStoreReferences()
	if s.webhookStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "webhook_store_unavailable", "Webhook store is unavailable")
		return
	}
	scope, ok := requireResourceScope(w, r)
	if !ok {
		return
	}
	delivery, err := s.webhookStore.GetDelivery(scope.OwnerUserID, strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeWebhookAPIError(w, err)
		return
	}
	encodeJSON(w, http.StatusOK, delivery)
}

func (s *Server) handleAPIWebhookReplay(w http.ResponseWriter, r *http.Request) {
	s.ensureSharedStoreReferences()
	if s.webhookStore == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "webhook_store_unavailable", "Webhook store is unavailable")
		return
	}
	scope, ok := requireResourceScope(w, r)
	if !ok {
		return
	}
	release, err := s.acquireResourceMutation(scope, true)
	if err != nil {
		writeResourceLifecycleError(w, err)
		return
	}
	delivery, err := s.webhookStore.Replay(scope.OwnerUserID, strings.TrimSpace(r.PathValue("id")))
	release()
	if err != nil {
		writeWebhookAPIError(w, err)
		return
	}
	s.publishWebhookDeliveryChanged(scope.OwnerUserID, delivery)
	if s.webhookDispatcher != nil {
		s.webhookDispatcher.Wake()
	}
	encodeJSON(w, http.StatusAccepted, delivery)
}

func (s *Server) publishWebhookChanged(ownerUserID, action, webhookID string) {
	if s.events == nil {
		return
	}
	s.events.PublishScopedJSON("webhook_changed", ownerUserID, map[string]string{"action": action, "webhook_id": webhookID})
}

func (s *Server) publishWebhookDeliveryChanged(ownerUserID string, delivery WebhookDelivery) {
	if s.events == nil {
		return
	}
	s.events.PublishScopedJSON("webhook_delivery_changed", ownerUserID, map[string]any{
		"webhook_id": delivery.WebhookID, "delivery_id": delivery.ID, "status": delivery.Status,
	})
}
func writeWebhookAPIError(w http.ResponseWriter, err error) {
	var validation *webhookValidationError
	switch {
	case errors.As(err, &validation):
		encodeJSON(w, http.StatusUnprocessableEntity, apiErrorResponse{Error: validation.Error(), Message: validation.Error(), Code: validation.Code, Field: validation.Field})
	case errors.Is(err, ErrWebhookNotFound), errors.Is(err, ErrWebhookDeliveryNotFound):
		writeAPIError(w, http.StatusNotFound, "webhook_not_found", err.Error())
	case errors.Is(err, ErrWebhookRevisionConflict):
		writeAPIError(w, http.StatusConflict, "webhook_revision_conflict", err.Error())
	case errors.Is(err, ErrWebhookLimitReached):
		writeAPIError(w, http.StatusConflict, "webhook_limit_reached", err.Error())
	case errors.Is(err, ErrWebhookDailyCapReached):
		writeAPIError(w, http.StatusTooManyRequests, "webhook_daily_cap_reached", err.Error())
	case errors.Is(err, ErrWebhookPendingFull):
		writeAPIError(w, http.StatusTooManyRequests, "webhook_pending_full", err.Error())
	case errors.Is(err, ErrWebhookReplayUnavailable):
		writeAPIError(w, http.StatusConflict, "webhook_replay_unavailable", err.Error())
	default:
		writeAPIError(w, http.StatusInternalServerError, "webhook_operation_failed", "Webhook operation failed")
	}
}
