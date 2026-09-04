package notifications

import (
	"net/http"
	"time"

	"github.com/zhushilin/blog-project/internal/platform/netguard"
)

func newGuardedHTTPClient() *http.Client {
	return netguard.NewClient(netguard.Options{
		Timeout:               15 * time.Second,
		ConnectTimeout:        5 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConns:          8,
	})
}
