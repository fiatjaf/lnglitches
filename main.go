package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/mux"
	"github.com/kelseyhightower/envconfig"
	"github.com/lnbits/lnbits/api"
	"github.com/lnbits/lnbits/apps"
	"github.com/lnbits/lnbits/lightning"
	"github.com/lnbits/lnbits/storage"
	"github.com/rs/cors"
	"github.com/rs/zerolog"
)

type Settings struct {
	Host     string `envconfig:"HOST" default:"0.0.0.0"`
	Port     string `envconfig:"PORT" default:"5000"`
	Database string `envconfig:"DATABASE" required:"true"`

	SiteTitle         string   `envconfig:"LNBITS_SITE_TITLE" default:"LNBitsLocal"`
	SiteTagline       string   `envconfig:"LNBITS_SITE_TAGLINE" default:"Locally-hosted lightning wallet"`
	SiteDescription   string   `envconfig:"LNBITS_SITE_DESCRIPTION" default:""`
	ThemeOptions      []string `envconfig:"LNBITS_THEME_OPTIONS" default:"classic, flamingo, mint, salvador, monochrome, autumn"`
	DefaultWalletName string   `envconfig:"LNBITS_DEFAULT_WALLET_NAME" default:"LNbits Wallet"`

	LightningBackend string `envconfig:"LNBITS_LIGHTNING_BACKEND" default:"void"`
	// -- other env vars are defined in the 'lightning' package
}

var s Settings
var log = zerolog.New(os.Stderr).Output(zerolog.ConsoleWriter{Out: os.Stdout})
var router = mux.NewRouter()
var commit string // will be set at compile time

//go:embed client/dist/spa
var static embed.FS

func main() {
	err := envconfig.Process("", &s)
	if err != nil {
		log.Fatal().Err(err).Msg("couldn't process envconfig.")
		return
	}

	// setup logger
	zerolog.SetGlobalLevel(zerolog.DebugLevel)
	log = log.With().Timestamp().Logger()

	// database
	if err := storage.Connect(s.Database); err != nil {
		log.Fatal().Err(err).Str("database", s.Database).
			Msg("couldn't open database.")
	}

	// lightning backend
	lightning.Connect(s.LightningBackend)
	if info, err := lightning.LN.GetInfo(); err != nil {
		log.Fatal().Err(err).Str("lightning", s.LightningBackend).
			Msg("couldn't start lightning backend.")
		return
	} else {
		log.Info().Int64("msat", info.Balance).Str("kind", s.LightningBackend).
			Msg("initialized lightning backend")
	}

	// serve http routes
	router.Path("/v/settings").HandlerFunc(viewSettings)
	router.Path("/api/user").HandlerFunc(api.User)
	router.Path("/api/user/create-wallet").HandlerFunc(api.CreateWallet)
	router.Path("/api/user/add-app").HandlerFunc(api.AddApp)
	router.Path("/api/wallet").HandlerFunc(api.Wallet)
	router.Path("/api/wallet/rename/{new-name}").HandlerFunc(api.RenameWallet)
	router.Path("/api/wallet/create-invoice").HandlerFunc(api.CreateInvoice)
	router.Path("/api/wallet/pay-invoice").HandlerFunc(api.PayInvoice)
	router.Path("/api/wallet/lnurlauth").HandlerFunc(api.LnurlAuth)
	router.Path("/api/wallet/pay-lnurl").HandlerFunc(api.PayLnurl)
	router.Path("/api/wallet/payment/{id}").HandlerFunc(api.GetPayment)
	router.Path("/api/wallet/lnurlscan/{code}").HandlerFunc(api.LnurlScan)
	router.Path("/api/wallet/sse").HandlerFunc(api.SSE)

	// app endpoints
	router.Path("/api/wallet/app/{appid}").HandlerFunc(apps.AppInfo)
	router.Path("/api/wallet/app/{appid}/list").HandlerFunc(apps.AppListItems)
	router.Path("/api/wallet/app/{appid}/set/{key}").HandlerFunc(apps.AppSetItem)
	router.Path("/api/wallet/app/{appid}/del/{key}").HandlerFunc(apps.AppDeleteItem)
	router.PathPrefix("/app/{wallet}/{appid}").HandlerFunc(apps.AppCustom)

	// lnbits compatibility routes (for lnbits libraries and lnbits wallets)
	router.Path("/api/v1/wallet").HandlerFunc(api.Wallet)
	router.Path("/api/v1/wallet/{new-name}").HandlerFunc(api.RenameWallet)
	router.Path("/api/v1/payments").MatcherFunc(
		func(r *http.Request, rm *mux.RouteMatch) bool {
			var outer struct {
				Out bool `json:"out"`
			}
			json.NewDecoder(r.Clone(r.Context()).Body).Decode(&outer)
			return !outer.Out
		},
	).HandlerFunc(api.CreateInvoice)
	router.Path("/api/v1/payments").MatcherFunc(
		func(r *http.Request, rm *mux.RouteMatch) bool {
			var outer struct {
				Out bool `json:"out"`
			}
			json.NewDecoder(r.Clone(r.Context()).Body).Decode(&outer)
			return outer.Out
		},
	).HandlerFunc(api.PayInvoice)
	router.Path("/api/v1/payments/lnurl").HandlerFunc(api.PayLnurl)
	router.Path("/api/v1/payments/{id}").HandlerFunc(api.GetPayment)
	router.Path("/api/v1/payments/sse").HandlerFunc(api.SSE)

	// middleware
	router.Use(jsonHeaderMiddleware)
	router.Use(userMiddleware)
	router.Use(walletMiddleware)
	router.Use(cors.AllowAll().Handler)

	// serve static client
	if staticFS, err := fs.Sub(static, "client/dist/spa"); err != nil {
		log.Fatal().Err(err).Msg("failed to load static files subdir")
		return
	} else {
		spaFS := SpaFS{staticFS}
		httpFS := http.FS(spaFS)
		router.PathPrefix("/").Handler(http.FileServer(httpFS))
	}

	// start http server
	log.Info().Str("host", s.Host+":"+s.Port).Msg("http listening")
	srv := &http.Server{
		Handler:      router,
		Addr:         s.Host + ":" + s.Port,
		WriteTimeout: 10 * time.Second,
		ReadTimeout:  10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Error().Err(err).Msg("error serving http")
	}
}

type SpaFS struct {
	base fs.FS
}

func (s SpaFS) Open(name string) (fs.File, error) {
	if file, err := s.base.Open(name); err == nil {
		return file, nil
	} else {
		return s.base.Open("index.html")
	}
}
