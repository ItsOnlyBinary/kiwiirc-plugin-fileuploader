package db

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/kiwiirc/plugin-fileuploader/config"
	"github.com/rs/zerolog"

	_ "github.com/go-sql-driver/mysql" // register mysql driver
	_ "github.com/mattn/go-sqlite3"    // register SQL driver
)

type DBConfig struct {
	DriverName string
	DSN        string
}

type DatabaseConnection struct {
	DB       *sqlx.DB
	DBConfig *DBConfig
	DBLock   sync.RWMutex

	cfg *config.Config
	log *zerolog.Logger

	newUploadStmt       *sql.Stmt
	finishUploadStmt    *sql.Stmt
	terminateUploadStmt *sql.Stmt
	duplicateIdsStmt    *sqlx.Stmt
	fetchUploadStmt     *sqlx.Stmt
	expiredIdsStmt      *sqlx.Stmt
}

func ConnectToDB(log *zerolog.Logger, cfg *config.Config) *DatabaseConnection {
	dbConfig := &DBConfig{
		DriverName: cfg.Database.Type,
		DSN:        cfg.Database.Path,
	}

	if !strings.Contains(dbConfig.DSN, "?") {
		// Add the default connection options if none are given
		switch dbConfig.DriverName {
		case "sqlite3":
			dbConfig.DSN += "?_busy_timeout=5000"
		case "mysql":
			dbConfig.DSN += "?parseTime=true"
		}
	}

	// note that we don't do db.SetMaxOpenConns(1), as we don't want to limit
	// read concurrency unnecessarily. sqlite will handle write locking on its
	// own, even across multiple processes accessing the same database file.
	// https://www.sqlite.org/faq.html#q5

	// we also don't enable the write-ahead-log because it does not work over a
	// networked filesystem

	db, err := sqlx.Open(dbConfig.DriverName, dbConfig.DSN)
	if err != nil {
		log.Fatal().Err(err).Msg("Could not open database")
	}

	dBConn := &DatabaseConnection{
		DB:       db,
		DBConfig: dbConfig,
		cfg:      cfg,
		log:      log,
	}

	dBConn.applyMigrations()

	// Prepare our sql statements
	newUploadStmt, err := db.Prepare(newUploadQuery)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to prepare newUploadStmt")
	}
	dBConn.newUploadStmt = newUploadStmt

	finishUploadStmt, err := db.Prepare(finishUploadQuery)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to prepare finishedUploadStmt")
	}
	dBConn.finishUploadStmt = finishUploadStmt

	terminateUploadStmt, err := db.Prepare(terminateUploadQuery)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to prepare terminateUploadStmt")
	}
	dBConn.terminateUploadStmt = terminateUploadStmt

	duplicateIdsStmt, err := db.Preparex(duplicateIdsQuery)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to prepare duplicateIdsStmt")
	}
	dBConn.duplicateIdsStmt = duplicateIdsStmt

	fetchUploadStmt, err := db.Preparex(fetchUploadQuery)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to prepare fetchUploadStmt")
	}
	dBConn.fetchUploadStmt = fetchUploadStmt

	expiredIdsStmt, err := db.Preparex(expiredIdsQuery)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to prepare expiredIdsStmt")
	}
	dBConn.expiredIdsStmt = expiredIdsStmt

	return dBConn
}

func (dBConn *DatabaseConnection) WriteStmt(stmt *sql.Stmt, args ...any) (sql.Result, error) {
	if dBConn.DBConfig.DriverName == "sqlite3" {
		dBConn.DBLock.Lock()
		defer dBConn.DBLock.Unlock()
	}
	res, err := stmt.Exec(args...)
	if err != nil {
		return nil, err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if count != 1 {
		err = fmt.Errorf("db expected 1 affected row, got %d", count)
		return nil, err
	}
	return res, err
}

func (dBConn *DatabaseConnection) ReadStmt(stmt *sqlx.Stmt, args ...any) (*sqlx.Rows, error) {
	if dBConn.DBConfig.DriverName == "sqlite3" {
		dBConn.DBLock.RLock()
		defer dBConn.DBLock.RUnlock()
	}
	return stmt.Queryx(args...)
}

func (dBConn *DatabaseConnection) ReadStmtRow(stmt *sqlx.Stmt, args ...any) *sqlx.Row {
	if dBConn.DBConfig.DriverName == "sqlite3" {
		dBConn.DBLock.RLock()
		defer dBConn.DBLock.RUnlock()
	}
	return stmt.QueryRowx(args...)
}

const newUploadQuery = `
	INSERT INTO uploads (id, created_at, uploader_ip, file_name, file_type, jwt_nick, jwt_account, jwt_issuer)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?);
`

func (dBConn *DatabaseConnection) NewUpload(id, uploader_ip, file_name, file_type, jwt_nick, jwt_account, jwt_issuer string) error {
	created_at := time.Now().Unix()
	_, err := dBConn.WriteStmt(dBConn.newUploadStmt, id, created_at, uploader_ip, file_name, file_type, jwt_nick, jwt_account, jwt_issuer)
	return err
}

const finishUploadQuery = `
	UPDATE uploads
	SET sha256sum = ?, expires_at = ?, file_size = ?
	WHERE id = ?
`

func (dBConn *DatabaseConnection) FinishUpload(id string, sha256sum []byte, expires_at, file_size int64) error {
	_, err := dBConn.WriteStmt(dBConn.finishUploadStmt, sha256sum, expires_at, file_size, id)
	return err
}

const terminateUploadQuery = `
	UPDATE uploads
	SET deleted_at = ?
	WHERE id = ?
`

func (dBConn *DatabaseConnection) TerminateUpload(id string, when int64) error {
	_, err := dBConn.WriteStmt(dBConn.terminateUploadStmt, when, id)
	return err
}

const duplicateIdsQuery = `
	SELECT count(id)
	FROM uploads
	WHERE id != $1
		AND deleted_at = -1
		AND sha256sum = (SELECT sha256sum FROM uploads WHERE id = $1);
`

func (dBConn *DatabaseConnection) FetchDuplicateIds(id string) (int, error) {
	var result int
	row := dBConn.ReadStmtRow(dBConn.duplicateIdsStmt, id)
	if err := row.Scan(&result); err != nil {
		return -1, err
	}
	return result, nil
}

const fetchUploadQuery = `
	SELECT *
	FROM uploads
	WHERE id = ?;
`

func (dBConn *DatabaseConnection) FetchUpload(id string) (map[string]interface{}, error) {
	result := map[string]interface{}{}
	row := dBConn.ReadStmtRow(dBConn.fetchUploadStmt, id)
	if err := row.MapScan(result); err != nil {
		return map[string]interface{}{}, err
	}
	return result, nil
}

const expiredIdsQuery = `
	SELECT id
	FROM uploads
	WHERE deleted_at = -1 AND (
		expires_at <= ? OR (expires_at IS NULL AND created_at <= ?)
	);
`

func (dBConn *DatabaseConnection) FetchExpiredIds() ([]string, error) {
	var result []string
	rows, err := dBConn.ReadStmt(dBConn.expiredIdsStmt,
		time.Now().Unix(),
		time.Now().Unix()-86400, // 1 day for incomplete uploads
	)
	if err != nil {
		return []string{}, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return []string{}, err
		}
		result = append(result, id)
	}
	return result, err
}
