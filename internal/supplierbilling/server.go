// Package supplierbilling implements an independent supplier settlement service.
// It deliberately does not import the gateway or Report Service user/session stores.
package supplierbilling

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	RoleManager  = "billing_manager"
	RoleSupplier = "supplier"
)

var businessZone = time.FixedZone("Asia/Shanghai", 8*60*60)

type Server struct {
	DB            *gorm.DB
	Origin        string
	SecureCookies bool
	SourceOrigins []string
}

// OpenDatabase uses a dedicated database. No Report Service/gateway migrations
// or configuration are loaded by this process.
func OpenDatabase(driver, dsn string) (*gorm.DB, error) {
	var dialect gorm.Dialector
	switch driver {
	case "sqlite":
		dialect = sqlite.Open(dsn)
	case "mysql":
		dialect = mysql.Open(dsn)
	case "postgres":
		dialect = postgres.Open(dsn)
	default:
		return nil, errors.New("unsupported database driver")
	}
	db, err := gorm.Open(dialect, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	pool, err := db.DB()
	if err != nil {
		return nil, err
	}
	if driver == "sqlite" {
		pool.SetMaxOpenConns(1)
	}
	return db, nil
}

func (s *Server) Migrate() error {
	models := []any{&Supplier{}, &Vendor{}, &VendorKey{}, &User{}, &Source{}, &SourceChannel{}, &SourceSync{}, &Usage{}, &Recharge{}, &LedgerEntry{}, &InitialBalance{}, &Claim{}}
	db := s.DB
	if db.Dialector.Name() == "mysql" {
		// Ledger transactions require InnoDB, and supplier names must not depend
		// on the MySQL server's legacy default character set.
		db = db.Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4")
	}
	return db.AutoMigrate(append(models, AuthModels()...)...)
}

func (s *Server) Handler(frontendDir string) http.Handler {
	mux := http.NewServeMux()
	s.RegisterAuth(mux)
	mux.HandleFunc("GET /api/suppliers", s.auth(s.listSuppliers))
	mux.HandleFunc("GET /api/vendors", s.auth(s.listVendors))
	mux.HandleFunc("POST /api/suppliers", s.manager(s.createSupplier))
	mux.HandleFunc("GET /api/statement", s.auth(s.statement))
	mux.HandleFunc("GET /api/daily-summary", s.auth(s.dailySummary))
	mux.HandleFunc("GET /api/overview", s.manager(s.overview))
	mux.HandleFunc("PUT /api/suppliers/{id}/initial-balance", s.manager(s.setInitialBalance))
	mux.HandleFunc("PUT /api/claims", s.manager(s.saveClaim))
	mux.HandleFunc("POST /api/usage", s.auth(s.createUsage))
	mux.HandleFunc("POST /api/usage/price", s.auth(s.priceUsage))
	mux.HandleFunc("POST /api/recharges", s.auth(s.createRecharge))
	mux.HandleFunc("POST /api/recharges/{id}/reverse", s.manager(s.reverseRecharge))
	mux.HandleFunc("DELETE /api/recharges/{id}", s.auth(s.deleteRecharge))
	mux.HandleFunc("DELETE /api/usage/{id}", s.auth(s.deleteUsage))
	mux.HandleFunc("GET /api/sources", s.manager(s.listSources))
	mux.HandleFunc("POST /api/sources", s.manager(s.createSource))
	mux.HandleFunc("POST /api/sources/discover", s.manager(s.discoverMapping))
	mux.HandleFunc("PUT /api/sources/{id}", s.manager(s.updateSource))
	mux.HandleFunc("POST /api/sources/{id}/sync", s.manager(s.syncSource))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		pool, err := s.DB.DB()
		if err != nil || pool.PingContext(r.Context()) != nil {
			s.fail(w, 503, "Service unavailable")
			return
		}
		s.write(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { s.fail(w, 404, "Not found") })
	files := http.FileServer(http.Dir(frontendDir))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			s.fail(w, http.StatusNotFound, "Not found")
			return
		}
		if frontendDir == "" {
			s.fail(w, 404, "Frontend build is not configured")
			return
		}
		path := filepath.Join(frontendDir, filepath.Clean("/"+r.URL.Path))
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			files.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(frontendDir, "billing.html"))
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if s.SecureCookies {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			if r.Header.Get("Origin") != s.Origin || r.Header.Get("X-Requested-With") != "supplier-billing" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				s.fail(w, http.StatusForbidden, "Invalid request origin")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) write(w http.ResponseWriter, status int, v any) {
	b, err := common.Marshal(v)
	if err != nil {
		http.Error(w, "Response encoding failed", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func (s *Server) fail(w http.ResponseWriter, status int, message string) {
	s.write(w, status, map[string]string{"error": message})
}

func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil || len(data) == 0 || strings.TrimSpace(string(data)) == "null" {
		s.fail(w, 400, "Invalid request")
		return false
	}
	if err := common.Unmarshal(data, v); err != nil {
		s.fail(w, 400, "Invalid request")
		return false
	}
	return true
}
