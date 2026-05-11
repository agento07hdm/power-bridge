package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fedzzito/power-bridge/internal/config"
	"github.com/fedzzito/power-bridge/internal/poweropti"
	"github.com/fedzzito/power-bridge/internal/server"
)

var version = "dev"

var (
	configFile  = flag.String("config", "/etc/power-bridge/config.yaml", "path to config file")
	listenAddr  = flag.String("listen", "", "HTTP listen address (overrides config, e.g. :8080)")
	showVersion = flag.Bool("version", false, "print version and exit")
)

func main() {
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		os.Exit(0)
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("power-bridge %s starting, config=%s", version, *configFile)

	server.Version = version

	cfg, err := config.Load(*configFile)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	config.ApplyAutodetect(cfg)

	if *listenAddr != "" {
		cfg.ListenAddr = *listenAddr
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":80"
	}

	var poller *poweropti.Client
	var cancelPoller context.CancelFunc
	var cancelBroadcaster context.CancelFunc

	if cfg.Configured {
		log.Printf("configured: poweropti=%s hostname=%s", cfg.PoweroptiIP, cfg.Hostname)
		ctx, cancel := context.WithCancel(context.Background())
		cancelPoller = cancel
		poller = poweropti.NewClient(cfg)
		go poller.Run(ctx)
	} else {
		log.Println("not configured – starting setup mode")
	}

	srv := server.New(cfg, *configFile, poller)

	if cfg.Configured && poller != nil {
		bctx, bcancel := context.WithCancel(context.Background())
		cancelBroadcaster = bcancel
		go srv.RunNotifyBroadcaster(bctx)
	}

	go func() {
		log.Printf("listening on %s", cfg.ListenAddr)
		if err := srv.Listen(cfg.ListenAddr); err != nil {
			log.Fatalf("server: %v", err)
		}
	}()

	// Start additional listeners for target systems that probe alternate ports
	// (e.g. Marstek / Hoymiles may probe port 1010 or 2220).
	for _, port := range cfg.ExtraPorts {
		port := port // capture loop variable
		go func() {
			addr := fmt.Sprintf(":%d", port)
			log.Printf("also listening on %s", addr)
			if err := srv.ListenOnPort(addr); err != nil {
				log.Printf("extra port %s: %v", addr, err)
			}
		}()
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down…")
	if cancelBroadcaster != nil {
		cancelBroadcaster()
	}
	if cancelPoller != nil {
		cancelPoller()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	log.Println("stopped")
}
