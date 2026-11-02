package main

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/internal/supplierbilling"
)

func main() {
	origin := strings.TrimRight(os.Getenv("BILLING_ORIGIN"), "/")
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		log.Fatal(common.LogText("BILLING_ORIGIN must be the exact public origin"))
	}
	local := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && local && os.Getenv("BILLING_ALLOW_LOCAL_HTTP") == "true") {
		log.Fatal(common.LogText("BILLING_ORIGIN requires HTTPS; local HTTP requires BILLING_ALLOW_LOCAL_HTTP=true"))
	}
	driver := os.Getenv("BILLING_DB_DRIVER")
	if driver == "" {
		driver = "postgres"
	}
	dsn := os.Getenv("BILLING_DB_DSN")
	if dsn == "" {
		log.Fatal(common.LogText("BILLING_DB_DSN must point to a dedicated supplier billing database"))
	}
	db, err := supplierbilling.OpenDatabase(driver, dsn)
	if err != nil {
		log.Fatal(common.LogText("Cannot connect to supplier billing database"))
	}
	pool, err := db.DB()
	if err != nil {
		log.Fatal(common.LogText("Cannot access supplier billing database connections"))
	}
	defer pool.Close()
	server := &supplierbilling.Server{DB: db, Origin: origin, SecureCookies: u.Scheme == "https"}
	for part := range strings.SplitSeq(os.Getenv("BILLING_SOURCE_ORIGINS"), ",") {
		if part = strings.TrimSpace(part); part != "" {
			server.SourceOrigins = append(server.SourceOrigins, strings.TrimRight(part, "/"))
		}
	}
	if err := server.Migrate(); err != nil {
		log.Fatal(common.LogText("Supplier billing migration failed"))
	}
	if err := server.BootstrapManager(os.Getenv("BILLING_ADMIN_USERNAME"), os.Getenv("BILLING_ADMIN_PASSWORD")); err != nil {
		log.Fatal(common.LogText("Supplier billing bootstrap failed: %s", err))
	}
	addr := os.Getenv("BILLING_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:8092"
	}
	frontend := os.Getenv("BILLING_FRONTEND_DIR")
	if frontend == "" {
		frontend = "scripts/report-service/frontend-v3/dist-billing"
	}
	httpServer := &http.Server{Addr: addr, Handler: server.Handler(frontend), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdown); err != nil {
			log.Print(common.LogText("Supplier billing shutdown did not finish: %s", err))
			_ = httpServer.Close()
		}
	}()
	log.Print(common.LogText("Supplier billing listening on %s", addr))
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(common.LogText("Supplier billing server stopped: %s", err))
	}
	<-shutdownDone
}
