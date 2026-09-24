package main

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/pkg/errors"
)

// connectPageData describes an authorization of an MCP server by a user.
type connectPageData struct {
	BasePath string
	NodeID   string
	Endpoint string
	Host     string
	Done     bool
	Error    string
}

// handleConnect asks the user to confirm the authorization: the link comes
// from a tool call, the server it names is shown before going further.
func handleConnect(w http.ResponseWriter, r *http.Request) {
	pd := connectPageData{
		BasePath: basePath(r),
		NodeID:   r.URL.Query().Get("nodeId"),
		Endpoint: r.URL.Query().Get("endpoint"),
	}
	endpoint, err := parseEndpoint(pd.Endpoint)
	if err != nil || pd.NodeID == "" {
		w.WriteHeader(http.StatusBadRequest)
		pd.Error = "Lien d'autorisation invalide."
	} else {
		pd.Host = endpoint.Host
	}
	renderTempl(w, r, connectPage(pd))
}

// handleStartConnect registers Xolo on the authorization server of the MCP
// server and sends the user there.
func (o *oauthClient) handleStartConnect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := r.Header.Get("X-Xolo-User-Id")
	publicBaseURL := r.Header.Get("X-Xolo-Public-Base-URL")
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pd := connectPageData{BasePath: basePath(r), NodeID: r.FormValue("nodeId"), Endpoint: r.FormValue("endpoint")}
	endpoint, err := parseEndpoint(pd.Endpoint)
	if err != nil || pd.NodeID == "" || userID == "" || publicBaseURL == "" {
		w.WriteHeader(http.StatusBadRequest)
		pd.Error = "Lien d'autorisation invalide."
		renderTempl(w, r, connectPage(pd))
		return
	}
	pd.Host = endpoint.Host

	redirectURI := strings.TrimSuffix(publicBaseURL, "/") + pd.BasePath + "callback"
	authURL, err := o.start(ctx, userID, r.Header.Get("X-Xolo-Org-Id"), pd.NodeID, pd.Endpoint, redirectURI)
	if err != nil {
		slog.WarnContext(ctx, "mcp-bridge/ui: could not start the authorization", slog.Any("error", err))
		w.WriteHeader(http.StatusBadGateway)
		pd.Error = "Le serveur MCP ne permet pas l'autorisation : " + err.Error()
		renderTempl(w, r, connectPage(pd))
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleCallback stores the token of the user coming back from the
// authorization server.
func (o *oauthClient) handleCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()
	pd := connectPageData{BasePath: basePath(r)}

	if e := query.Get("error"); e != "" {
		w.WriteHeader(http.StatusForbidden)
		pd.Error = "L'autorisation a été refusée (" + e + ")."
		renderTempl(w, r, connectPage(pd))
		return
	}

	host, pluginName := o.host(ctx)
	if host == nil {
		http.Error(w, "missing host context", http.StatusInternalServerError)
		return
	}
	pending, err := o.complete(ctx, host, pluginName, r.Header.Get("X-Xolo-User-Id"), query.Get("state"), query.Get("code"))
	if err != nil {
		slog.WarnContext(ctx, "mcp-bridge/ui: could not complete the authorization", slog.Any("error", err))
		status := http.StatusBadGateway
		if errors.Is(err, ErrUnknownAuthorization) {
			status = http.StatusBadRequest
		}
		w.WriteHeader(status)
		pd.Error = "L'autorisation n'a pas pu aboutir. Recommencez depuis le lien donné par l'assistant."
		renderTempl(w, r, connectPage(pd))
		return
	}

	pd.Done, pd.NodeID, pd.Endpoint = true, pending.nodeID, pending.token.Endpoint
	if endpoint, err := parseEndpoint(pd.Endpoint); err == nil {
		pd.Host = endpoint.Host
	}
	renderTempl(w, r, connectPage(pd))
}

func parseEndpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("invalid endpoint")
	}
	return u, nil
}

func basePath(r *http.Request) string {
	if p := r.Header.Get("X-Xolo-Plugin-Base-Path"); p != "" {
		return p
	}
	return "/"
}
