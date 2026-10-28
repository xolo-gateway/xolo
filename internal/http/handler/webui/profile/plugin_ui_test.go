package profile

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	"github.com/xolo-gateway/xolo/internal/i18n"
	"golang.org/x/text/language"
)

type pluginUIPorts uint32

func (p pluginUIPorts) HTTPPort(string) uint32 { return uint32(p) }

func TestPluginUITrustedContext(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/config", r.URL.Path)
		_ = json.NewEncoder(w).Encode(r.Header)
	}))
	defer upstream.Close()
	_, portString, err := net.SplitHostPort(upstream.Listener.Addr().String())
	require.NoError(t, err)
	portNum, err := strconv.Atoi(portString)
	require.NoError(t, err)
	locales, err := i18n.Load()
	require.NoError(t, err)
	tenant := model.NewTenant("tenant", "Tenant", "")
	user := model.NewUser(tenant.ID(), "test", "user", "u@test", "User", true, "admin")

	h := &Handler{pluginManager: pluginUIPorts(portNum)}
	for _, node := range []string{"", "unsaved-node"} {
		t.Run("node="+node, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/?nodeId="+node, nil)
			req.SetPathValue("pluginName", "pseudonymizer")
			req.SetPathValue("orgSlug", "org")
			req.SetPathValue("uiPath", "config")
			for _, key := range []string{"X-Xolo-Org-Id", "X-Xolo-Secret-Scope-Id", "X-Xolo-User-Id", "X-Xolo-Node-Id", "X-Xolo-Plugin-Base-Path", "X-Xolo-Public-Base-URL", "X-Xolo-Locale", "X-Xolo-Untrusted"} {
				req.Header.Set(key, "forged")
			}
			// Rewrite must also survive malicious hop-by-hop header names.
			req.Header.Set("Connection", "X-Xolo-Secret-Scope-Id, X-Xolo-Node-Id")
			ctx := httpCtx.SetBaseURL(httpCtx.SetUser(httpCtx.SetTenant(req.Context(), tenant), user), "https://tenant.example.test")
			ctx = locales.WithLanguage(ctx, language.Spanish)
			rec := httptest.NewRecorder()
			h.servePersonalPluginUI(rec, req.WithContext(ctx))
			require.Equal(t, http.StatusOK, rec.Code)
			var headers http.Header
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &headers))
			require.Equal(t, "~:"+string(user.ID()), headers.Get("X-Xolo-Secret-Scope-Id"))
			require.Equal(t, "~:"+string(user.ID()), headers.Get("X-Xolo-Org-Id"))
			require.Equal(t, string(user.ID()), headers.Get("X-Xolo-User-Id"))
			require.Equal(t, node, headers.Get("X-Xolo-Node-Id"))
			require.Equal(t, "https://tenant.example.test", headers.Get("X-Xolo-Public-Base-URL"))
			require.Equal(t, "es", headers.Get("X-Xolo-Locale"))
			require.NotEqual(t, "forged", headers.Get("X-Xolo-Plugin-Base-Path"))
			require.Empty(t, headers.Get("X-Xolo-Untrusted"))
		})
	}
	// A platform admin from another tenant cannot configure a personal/org node.
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("pluginName", "pseudonymizer")
	req.SetPathValue("orgSlug", "org")
	ctx := httpCtx.SetUser(httpCtx.SetTenant(req.Context(), model.NewTenant("other", "Other", "")), user)
	rec := httptest.NewRecorder()
	h.servePersonalPluginUI(rec, req.WithContext(ctx))
	require.Equal(t, http.StatusNotFound, rec.Code)
}
