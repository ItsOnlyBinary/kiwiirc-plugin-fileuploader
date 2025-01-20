package expirer

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/kiwiirc/plugin-fileuploader/db"
	"github.com/rs/zerolog"
	tusd "github.com/tus/tusd/v2/pkg/handler"
)

type Expirer struct {
	ticker   *time.Ticker
	quitChan chan struct{} // closes when ticker has been stopped
	composer *tusd.StoreComposer
	handler  *tusd.UnroutedHandler
	dbConn   *db.DatabaseConnection
	log      *zerolog.Logger
}

func New(composer *tusd.StoreComposer, handler *tusd.UnroutedHandler, dbConn *db.DatabaseConnection, checkInterval time.Duration, log *zerolog.Logger) *Expirer {
	expirer := &Expirer{
		ticker:   time.NewTicker(checkInterval),
		quitChan: make(chan struct{}),
		composer: composer,
		handler:  handler,
		dbConn:   dbConn,
		log:      log,
	}

	go func() {
		for {
			select {

			// tick
			case t := <-expirer.ticker.C:
				expirer.gc(t)

			// ticker stopped, exit the goroutine
			case _, ok := <-expirer.quitChan:
				if !ok {
					return
				}

			}
		}
	}()

	return expirer
}

// Stop turns off an Expirer. No more Filestore garbage collection cycles will start.
func (expirer *Expirer) Stop() {
	expirer.ticker.Stop()
	close(expirer.quitChan)
}

// TODO add cleanup of old database entries with config for age
func (expirer *Expirer) gc(t time.Time) {
	expirer.log.Debug().
		Str("event", "gc_tick").
		Msg("Filestore GC tick")

	expiredIds, err := expirer.dbConn.FetchExpiredIds()
	if err != nil {
		expirer.log.Error().
			Err(err).
			Msg("Failed to enumerate expired uploads")
		return
	}

	for _, id := range expiredIds {
		resp := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodDelete, id, nil)

		expirer.handler.DelFile(resp, req)

		if resp.Code == 404 {
			// Upload is not found, must have already been removed
			expirer.dbConn.TerminateUpload(id, 0)
		} else if resp.Code != 204 {
			expirer.log.Error().
				Err(errors.New(resp.Body.String())).
				Msg("Failed to terminate upload")
			continue
		}

		expirer.log.Info().
			Str("event", "expired").
			Str("id", id).
			Msg("Terminated upload id")

		time.Sleep(1 * time.Second)
	}
}
