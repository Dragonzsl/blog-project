package analytics

import (
	"context"
	"net/http"

	"github.com/zhushilin/blog-project/internal/extensions"
)

type Plugin struct {
	handler *HTTPHandler
}

func NewPlugin(handler *HTTPHandler) *Plugin { return &Plugin{handler: handler} }

func (p *Plugin) Manifest() extensions.Manifest {
	return extensions.Manifest{
		ID: "analytics.local", Name: "本地统计", Version: "1.0.0",
		APIVersion: extensions.HostAPIVersion, Kind: "analytics",
		Capabilities: []string{"admin_menu", "persistent_task"},
	}
}

func (p *Plugin) Register(host *extensions.Host) error {
	if p.handler == nil || p.handler.service == nil {
		return extensions.ErrCapabilityDenied
	}
	if err := host.RegisterSettings(extensions.SettingsSchema{
		"retention_days": {Type: "integer", Default: 365, Min: floatPtr(30), Max: floatPtr(3650)},
	}); err != nil {
		return err
	}
	if err := host.RegisterMenu(extensions.MenuItem{Label: "访问统计", Path: "/admin/plugins/analytics.local/analytics", Section: "system", Order: 40}); err != nil {
		return err
	}
	if err := host.AdminRoute(http.MethodGet, "/analytics", p.handler.dashboard); err != nil {
		return err
	}
	return host.RegisterTask("retention", func(ctx context.Context, _ []byte) error {
		_, err := p.handler.service.Purge(ctx)
		return err
	})
}

func floatPtr(value float64) *float64 { return &value }
