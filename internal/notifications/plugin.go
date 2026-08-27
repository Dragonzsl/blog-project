package notifications

import (
	"encoding/json"
	"net/http"

	"github.com/zhushilin/blog-project/internal/extensions"
)

type NewsletterHTTPHandler struct {
	service NewsletterService
}

func NewNewsletterHTTPHandler(service NewsletterService) *NewsletterHTTPHandler {
	return &NewsletterHTTPHandler{service: service}
}

func (h *NewsletterHTTPHandler) subscribe(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		writeNewsletterJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid form"})
		return
	}
	if err := h.service.Subscribe(r.Context(), r.FormValue("email")); err != nil {
		writeNewsletterJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeNewsletterJSON(w, http.StatusAccepted, map[string]string{"status": "subscribed"})
}

func (h *NewsletterHTTPHandler) unsubscribe(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		writeNewsletterJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid form"})
		return
	}
	if err := h.service.Unsubscribe(r.Context(), r.FormValue("email")); err != nil {
		writeNewsletterJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
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
}

func (p *NewsletterPlugin) Manifest() extensions.Manifest {
	return extensions.Manifest{ID: p.ID, Name: p.Name, Version: "1.0.0", APIVersion: extensions.HostAPIVersion, Kind: "newsletter", Capabilities: []string{"public_route"}}
}

func (p *NewsletterPlugin) Register(host *extensions.Host) error {
	if p.Handler == nil {
		return extensions.ErrCapabilityDenied
	}
	if err := host.RegisterSettings(extensions.SettingsSchema{"enabled": {Type: "boolean", Default: true}}); err != nil {
		return err
	}
	if err := host.Route(http.MethodPost, "/newsletter/subscribe", p.Handler.subscribe); err != nil {
		return err
	}
	return host.Route(http.MethodPost, "/newsletter/unsubscribe", p.Handler.unsubscribe)
}
