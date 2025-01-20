package db

import (
	"fmt"

	migrate "github.com/rubenv/sql-migrate"
)

func (dBConn *DatabaseConnection) applyMigrations() {
	migrations := &migrate.MemoryMigrationSource{
		Migrations: []*migrate.Migration{
			{
				Id: "1",
				Up: []string{
					`
					CREATE TABLE uploads(
						id VARCHAR(36) PRIMARY KEY,
						uploader_ip BLOB,
						sha256sum BLOB,
						created_at INTEGER(8)
					);`,
				},
				Down: []string{"DROP TABLE uploads;"},
			},
			{
				Id: "2",
				Up: []string{
					`
					ALTER TABLE uploads
						ADD deleted INTEGER(1) DEFAULT 0 NOT NULL
					;`,
				},
			},
			{
				Id: "3",
				Up: []string{
					`
					CREATE TABLE new_uploads(
						id VARCHAR(36) PRIMARY KEY,
						uploader_ip VARCHAR(45),
						sha256sum BLOB,
						created_at INTEGER(8),
						deleted INTEGER(1) DEFAULT 0 NOT NULL
					);`,
					`
					INSERT INTO new_uploads(id, sha256sum, created_at, deleted)
						SELECT id, sha256sum, created_at, deleted
						FROM uploads
					;`,
					`DROP TABLE uploads;`,
					`ALTER TABLE new_uploads RENAME TO uploads;`,
				},
			},
			{
				Id: "4",
				Up: []string{
					`
					CREATE TABLE new_uploads(
						id VARCHAR(36) PRIMARY KEY,
						uploader_ip VARCHAR(45),
						sha256sum BLOB,
						created_at INTEGER(8),
						deleted INTEGER(1) DEFAULT 0 NOT NULL,
						jwt_account TEXT,
						jwt_issuer TEXT
					);`,
					`
					INSERT INTO new_uploads(id, uploader_ip, sha256sum, created_at, deleted)
						SELECT id, uploader_ip, sha256sum, created_at, deleted
						FROM uploads
					;`,
					`DROP TABLE uploads;`,
					`ALTER TABLE new_uploads RENAME TO uploads;`,
				},
			},
			{
				Id: "5",
				Up: []string{
					`
					CREATE TABLE new_uploads(
						id VARCHAR(36) PRIMARY KEY,
						uploader_ip VARCHAR(45),
						sha256sum BLOB,
						created_at INTEGER(8),
						expires_at INTEGER(8),
						deleted INTEGER(1) DEFAULT 0 NOT NULL,
						jwt_account TEXT DEFAULT '' NOT NULL,
						jwt_issuer TEXT DEFAULT '' NOT NULL
					);`,
					`
					INSERT INTO new_uploads(id, uploader_ip, sha256sum, created_at, deleted, jwt_account, jwt_issuer, expires_at)
						SELECT id, uploader_ip, sha256sum, created_at, deleted,
						CASE WHEN jwt_account IS NULL THEN '' ELSE jwt_account END,
						CASE WHEN jwt_issuer IS NULL THEN '' ELSE jwt_issuer END,
						CASE WHEN jwt_account IS NOT NULL THEN created_at + ` + fmt.Sprintf("%.0f", dBConn.cfg.Expiration.IdentifiedMaxAge.Duration.Seconds()) + `
						ELSE created_at + ` + fmt.Sprintf("%.0f", dBConn.cfg.Expiration.MaxAge.Duration.Seconds()) + ` END
					 	as expires_at
						FROM uploads
					;`,
					`DROP TABLE uploads;`,
					`ALTER TABLE new_uploads RENAME TO uploads;`,
				},
			},
			{
				Id: "6",
				Up: []string{
					`
					CREATE TABLE new_uploads(
						id VARCHAR(36) PRIMARY KEY,
						uploader_ip VARCHAR(45) NOT NULL,
						sha256sum BLOB,
						created_at INTEGER NOT NULL,
						expires_at INTEGER DEFAULT -1 NOT NULL,
						deleted_at INTEGER DEFAULT -1 NOT NULL,
						file_name TEXT DEFAULT '' NOT NULL,
						file_type TEXT DEFAULT '' NOT NULL,
						file_size BIGINT TEXT DEFAULT -1 NOT NULL,
						jwt_nick TEXT DEFAULT '' NOT NULL,
						jwt_account TEXT DEFAULT '' NOT NULL,
						jwt_issuer TEXT DEFAULT '' NOT NULL
					);`,
					`
					INSERT INTO new_uploads(id, uploader_ip, sha256sum, created_at, expires_at, deleted_at, jwt_account, jwt_issuer)
						SELECT id, uploader_ip, sha256sum, created_at, expires_at, deleted, jwt_account, jwt_issuer
						FROM uploads
					;`,
					`DROP TABLE uploads;`,
					`ALTER TABLE new_uploads RENAME TO uploads;`,
				},
			},
		},
	}

	n, err := migrate.Exec(dBConn.DB.DB, dBConn.DBConfig.DriverName, migrations, migrate.Up)
	if err != nil {
		dBConn.log.Fatal().Err(err).Msg("Failed to apply migrations")
	}

	if n > 0 {
		dBConn.log.Info().
			Str("event", "schema_migrations").
			Int("count", n).Msg("Applied schema migrations")
	}
}
