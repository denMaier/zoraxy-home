package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	plugin "zoraxyhome/mod/zoraxy_plugin"
)

const (
	PLUGIN_ID = "io.github.denmaier.zoraxy.home"
	UI_PATH   = "/ui"
	DATA_DIR  = "./data"
)

//go:embed web
var webFS embed.FS

func main() {
	runtimeCfg, err := plugin.ServeAndRecvSpec(&plugin.IntroSpect{
		ID:            PLUGIN_ID,
		Name:          "Home",
		Author:        "Denis Maier",
		AuthorContact: "https://github.com/denMaier",
		Description:   "Turns your active HTTP proxy rules into a phone-style homepage with OIDC sign-in (Pocket ID, Authentik, Keycloak, …), per-user layouts, folders and themes",
		URL:           "https://github.com/denMaier/zoraxy-home",
		Type:          plugin.PluginType_Utilities,
		VersionMajor:  0,
		VersionMinor:  2,
		VersionPatch:  0,

		UIPath: UI_PATH,

		PermittedAPIEndpoints: []plugin.PermittedAPIEndpoint{
			{
				Method:   http.MethodGet,
				Endpoint: "/plugin/api/proxy/list",
				Reason:   "Reads the HTTP proxy rules that are shown as apps on the homepage",
			},
		},
	})
	if err != nil {
		fmt.Println("Error receiving configure spec:", err)
		os.Exit(1)
	}

	cfg, err := LoadConfig(DATA_DIR)
	if err != nil {
		log.Fatalf("[home] loading config: %v", err)
	}
	users, err := LoadUserStore(DATA_DIR)
	if err != nil {
		log.Fatalf("[home] loading users: %v", err)
	}
	accounts, err := LoadAccountStore(DATA_DIR)
	if err != nil {
		log.Fatalf("[home] loading accounts: %v", err)
	}

	routes := NewRouteSource(runtimeCfg.ZoraxyPort, runtimeCfg.APIKey)
	go routes.Run(30 * time.Second)

	app := &App{
		cfg:       cfg,
		users:     users,
		accounts:  accounts,
		routes:    routes,
		auth:      NewAuthenticator(cfg),
		ipLimit:   newLimiter(10, 15*time.Minute),
		userLimit: newLimiter(5, 15*time.Minute),
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	// Plugin UI (served inside the Zoraxy admin panel)
	uiMux := http.NewServeMux()
	app.RegisterPluginUI(uiMux)
	// Zoraxy asks plugins to stop gracefully via GET {UIPath}/term
	uiMux.HandleFunc(UI_PATH+"/term", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		select {
		case sig <- syscall.SIGTERM:
		default:
		}
	})
	pluginSrv := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", runtimeCfg.Port),
		Handler: uiMux,
	}
	go func() {
		log.Printf("[home] plugin UI on %s", pluginSrv.Addr)
		if err := pluginSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[home] plugin UI: %v", err)
		}
	}()

	// Public homepage, put behind a Zoraxy HTTP proxy rule
	homeMux := http.NewServeMux()
	app.RegisterHomepage(homeMux)
	homeSrv := &http.Server{
		Addr:              cfg.Get().ListenAddr,
		Handler:           securityHeaders(homeMux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("[home] homepage on %s", homeSrv.Addr)
		if err := homeSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[home] homepage listener failed: %v", err)
		}
	}()

	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	homeSrv.Shutdown(ctx)
	pluginSrv.Shutdown(ctx)
}
