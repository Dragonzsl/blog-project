package comments

import (
	"net/http"

	"github.com/zhushilin/blog-project/internal/extensions"
)

// LocalPlugin wires the built-in, SQLite-backed comment provider through the
// extension host. The provider is disabled by default and can be enabled with
// comments.enabled=true (or extensions.enabled_plugins).
type LocalPlugin struct {
	handler *HTTPHandler
}

func NewLocalPlugin(handler *HTTPHandler) *LocalPlugin { return &LocalPlugin{handler: handler} }

func (p *LocalPlugin) Manifest() extensions.Manifest {
	return extensions.Manifest{
		ID: "comments.local", Name: "本地评论", Version: "1.0.0",
		APIVersion: extensions.HostAPIVersion, Kind: "comment_provider",
		Capabilities: []string{"public_route", "admin_menu"},
	}
}

func (p *LocalPlugin) Register(host *extensions.Host) error {
	if p.handler == nil {
		return extensions.ErrCapabilityDenied
	}
	if err := host.RegisterSettings(extensions.SettingsSchema{
		"require_moderation": {Type: "boolean", Default: true},
	}); err != nil {
		return err
	}
	if err := host.RegisterMenu(extensions.MenuItem{Label: "评论", Path: "/admin/plugins/comments.local/comments", Section: "content", Order: 30}); err != nil {
		return err
	}
	if err := host.RouteSlot("comments", http.MethodGet, "/posts/{slug}/comments", p.handler.list); err != nil {
		return err
	}
	if err := host.RouteSlot("comments", http.MethodPost, "/posts/{slug}/comments", p.handler.create); err != nil {
		return err
	}
	if err := host.AdminRoute(http.MethodGet, "/comments", p.handler.pending); err != nil {
		return err
	}
	return host.AdminRoute(http.MethodPost, "/comments/{commentID}/moderate", p.handler.moderate)
}

// ExternalPlugin is intentionally a small adapter: the provider owns the
// comment UI and storage, while the core only exposes a safe outbound link.
type ExternalPlugin struct {
	handler *ExternalHTTPHandler
}

func NewExternalPlugin(endpoint string) *ExternalPlugin {
	return &ExternalPlugin{handler: &ExternalHTTPHandler{Endpoint: endpoint}}
}

func (p *ExternalPlugin) Manifest() extensions.Manifest {
	return extensions.Manifest{
		ID: "comments.external", Name: "外部评论", Version: "1.0.0",
		APIVersion: extensions.HostAPIVersion, Kind: "comment_provider",
		Capabilities: []string{"public_route"},
	}
}

func (p *ExternalPlugin) Register(host *extensions.Host) error {
	if p.handler == nil {
		return extensions.ErrCapabilityDenied
	}
	if err := host.RegisterSettings(extensions.SettingsSchema{
		"endpoint": {Type: "url", Required: true},
	}); err != nil {
		return err
	}
	return host.RouteSlot("comments", http.MethodGet, "/posts/{slug}/comments", p.handler.show)
}
