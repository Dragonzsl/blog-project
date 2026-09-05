package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/zhushilin/blog-project/internal/extensions"
	"github.com/zhushilin/blog-project/internal/platform/clientip"
	"github.com/zhushilin/blog-project/internal/platform/publicwrite"
)

type NewsletterHTTPHandler struct {
	service  *NewsletterService
	clientIP *clientip.Resolver
}

// NewNewsletterHTTPHandler keeps the original value-based constructor for
// small integrations and tests. NewNewsletterHTTPHandlerFromService should be
// used by the application so the handler and plugin share the same service.
func NewNewsletterHTTPHandler(service NewsletterService) *NewsletterHTTPHandler {
	return NewNewsletterHTTPHandlerFromService(&service)
}

func NewNewsletterHTTPHandlerFromService(service *NewsletterService) *NewsletterHTTPHandler {
	return &NewsletterHTTPHandler{service: service, clientIP: clientip.DirectPeerOnly()}
}

func (h *NewsletterHTTPHandler) SetClientIPResolver(resolver *clientip.Resolver) {
	if resolver == nil {
		resolver = clientip.DirectPeerOnly()
	}
	h.clientIP = resolver
}

func (h *NewsletterHTTPHandler) subscribe(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		writeNewsletterJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "newsletter is unavailable"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		writeNewsletterJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid form"})
		return
	}
	identity := clientip.DirectPeerOnly().Resolve(r)
	if h.clientIP != nil {
		identity = h.clientIP.Resolve(r)
	}
	err := h.service.RequestSubscribe(r.Context(), r.FormValue("email"), NewsletterRequest{
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		ClientIdentity: identity,
	})
	if err != nil {
		if errors.Is(err, publicwrite.ErrDuplicateRequest) && r.Header.Get("Idempotency-Key") == "" {
			writeNewsletterJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
			return
		}
		status := http.StatusUnprocessableEntity
		if errors.Is(err, publicwrite.ErrIdempotencyConflict) || errors.Is(err, publicwrite.ErrDuplicateRequest) {
			status = http.StatusConflict
		}
		writeNewsletterJSON(w, status, map[string]string{"error": "subscription request could not be accepted"})
		return
	}
	// Do not reveal whether the address already exists or was previously
	// unsubscribed. Confirmation is delivered asynchronously when applicable.
	writeNewsletterJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (h *NewsletterHTTPHandler) confirm(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		writeNewsletterJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "newsletter is unavailable"})
		return
	}
	result, err := h.service.Confirm(r.Context(), r.URL.Query().Get("token"))
	if err != nil {
		writeNewsletterJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid or expired newsletter token"})
		return
	}
	writeNewsletterJSON(w, http.StatusOK, map[string]string{"status": "confirmed", "unsubscribe_url": result.UnsubscribeURL})
}

func (h *NewsletterHTTPHandler) unsubscribe(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		writeNewsletterJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "newsletter is unavailable"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		writeNewsletterJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid form"})
		return
	}
	if err := h.service.UnsubscribeToken(r.Context(), r.FormValue("token")); err != nil {
		writeNewsletterJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid or expired newsletter token"})
		return
	}
	writeNewsletterJSON(w, http.StatusAccepted, map[string]string{"status": "unsubscribed"})
}

func writeNewsletterJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type NewsletterPlugin struct {
	ID      string
	Name    string
	Handler *NewsletterHTTPHandler
	Service *NewsletterService
}

func (p *NewsletterPlugin) Manifest() extensions.Manifest {
	return extensions.Manifest{ID: p.ID, Name: p.Name, Version: "1.0.0", APIVersion: extensions.HostAPIVersion, Kind: "newsletter", Capabilities: []string{"public_route", "persistent_task"}}
}

func (p *NewsletterPlugin) Register(host *extensions.Host) error {
	if p == nil || p.Handler == nil {
		return extensions.ErrCapabilityDenied
	}
	service := p.Service
	if service == nil {
		service = p.Handler.service
	}
	if service == nil {
		return errors.New("newsletter service is required")
	}
	p.Service = service
	service.SetTaskEnqueuer(func(ctx context.Context, tx *sql.Tx, kind string, payload any, idempotencyKey string, availableAt time.Time) error {
		return host.EnqueueTaskTx(ctx, tx, kind, payload, idempotencyKey, availableAt)
	})
	if err := host.RegisterSettings(extensions.SettingsSchema{"enabled": {Type: "boolean", Default: true}}); err != nil {
		return err
	}
	if err := host.RouteSlot("newsletter", http.MethodPost, "/newsletter/subscribe", p.Handler.subscribe); err != nil {
		return err
	}
	if err := host.RouteSlot("newsletter", http.MethodGet, "/newsletter/confirm", p.Handler.confirm); err != nil {
		return err
	}
	if err := host.RouteSlot("newsletter", http.MethodGet, "/newsletter/unsubscribe", p.Handler.unsubscribe); err != nil {
		return err
	}
	if err := host.RouteSlot("newsletter", http.MethodPost, "/newsletter/unsubscribe", p.Handler.unsubscribe); err != nil {
		return err
	}
	if err := host.RegisterTask("send_confirmation", service.ProcessConfirmationTask); err != nil {
		return err
	}
	return host.RegisterTask("sync", service.ProcessSyncTask)
}
