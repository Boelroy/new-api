package store

import (
	"database/sql"
	"log"

	_ "github.com/lib/pq"
)

var DB *sql.DB

func Init(dsn string) {
	var err error
	DB, err = sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("store: open db: %v", err)
	}
	if err = DB.Ping(); err != nil {
		log.Fatalf("store: ping db: %v", err)
	}
	migrate()
	log.Println("store: ready")
}

func migrate() {
	stmts := []string{
		// Maps a proxy user → remote channel id they own.
		// remote_channel_id is the channel id on the upstream new-api.
		`CREATE TABLE IF NOT EXISTS proxy_channel_ownership (
			remote_channel_id BIGINT NOT NULL,
			user_id           BIGINT NOT NULL,
			created_at        BIGINT NOT NULL,
			PRIMARY KEY (remote_channel_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pco_user ON proxy_channel_ownership(user_id)`,
	}
	for _, s := range stmts {
		if _, err := DB.Exec(s); err != nil {
			log.Fatalf("store: migrate: %v — stmt: %.80s", err, s)
		}
	}
}
