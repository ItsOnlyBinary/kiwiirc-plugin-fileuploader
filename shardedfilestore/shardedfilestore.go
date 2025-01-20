// Package shardedfilestore is a modified version of the tusd/filestore implementation.
// Splits file storage into subdirectories based on the hash prefix.
// based on https://github.com/tus/tusd/blob/c5d5b0a0422db85e9aa41b0cfa6e34926d25d224/pkg/filestore/filestore.go

// Package filestore provide a storage backend based on the local file system.
//
// FileStore is a storage backend used as a handler.DataStore in handler.NewHandler.
// It stores the uploads in a directory specified in two different files: The
// `[id].info` files are used to store the fileinfo in JSON format. The
// `[id]` files without an extension contain the raw binary data uploaded.
// No cleanup is performed so you may want to run a cronjob to ensure your disk
// is not filled up with old and finished uploads.
//
// Related to the filestore is the package filelocker, which provides a file-based
// locking mechanism. The use of some locking method is recommended and further
// explained in https://tus.github.io/tusd/advanced-topics/locks/.
package shardedfilestore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/kiwiirc/plugin-fileuploader/config"
	"github.com/kiwiirc/plugin-fileuploader/db"
	"github.com/kiwiirc/plugin-fileuploader/utils"
	"github.com/rs/zerolog"
	"github.com/tus/tusd/v2/pkg/handler"
)

var defaultFilePerm = os.FileMode(0664)
var defaultDirectoryPerm = os.FileMode(0754)

// See the handler.DataStore interface for documentation about the different
// methods.
type ShardedFileStore struct {
	// Relative or absolute path to store files in. FileStore does not check
	// whether the path exists, use os.MkdirAll in this case on your own.
	BasePath             string
	ShardLayers          int                       // Number of extra directory layers to prefix file paths with.
	ExpireTime           time.Duration             // How long before an upload expires (seconds)
	ExpireIdentifiedTime time.Duration             // How long before an upload expires with valid account (seconds)
	PreFinishCommands    []config.PreFinishCommand // Commands to run upon upload completion
	dBConn               *db.DatabaseConnection    // Database interface
	log                  *zerolog.Logger           // Log interface
}

// New creates a new file based storage backend. The directory specified will
// be used as the only storage entry. This method does not check
// whether the path exists, use os.MkdirAll to ensure.
func New(basePath string, shardLayers int, expireTime, expireIdentifiedTime time.Duration, preFinishCommands []config.PreFinishCommand, dbConn *db.DatabaseConnection, log *zerolog.Logger) *ShardedFileStore {
	store := &ShardedFileStore{
		BasePath:             basePath,
		ShardLayers:          shardLayers,
		ExpireTime:           expireTime,
		ExpireIdentifiedTime: expireIdentifiedTime,
		PreFinishCommands:    preFinishCommands,
		dBConn:               dbConn,
		log:                  log,
	}
	return store
}

// UseIn sets this store as the core data store in the passed composer and adds
// all possible extension to it.
func (store ShardedFileStore) UseIn(composer *handler.StoreComposer) {
	composer.UseCore(store)
	composer.UseTerminater(store)
	composer.UseConcater(store)
	composer.UseLengthDeferrer(store)
}

func (store ShardedFileStore) NewUpload(ctx context.Context, info handler.FileInfo) (handler.Upload, error) {
	if info.ID == "" {
		info.ID = utils.Uid()
	}

	// The .info file's location can directly be deduced from the upload ID
	infoPath := store.getShardedPath("meta", info.ID, "info")
	binPath := store.getPath("incomplete", info.ID, "bin")

	info.Storage = map[string]string{
		"Type":     "shardedfilestore",
		"Path":     binPath,
		"InfoPath": infoPath,
	}

	// if err := os.MkdirAll(filepath.Dir(infoPath), defaultDirectoryPerm); err != nil {
	// 	return nil, err
	// }

	// if err := os.MkdirAll(filepath.Dir(binPath), defaultDirectoryPerm); err != nil {
	// 	return nil, err
	// }

	// Create binary file with no content
	if err := createFile(binPath, nil); err != nil {
		return nil, err
	}

	upload := &fileUpload{
		info:     info,
		infoPath: infoPath,
		binPath:  binPath,
	}

	// writeInfo creates the file by itself if necessary
	if err := upload.writeInfo(); err != nil {
		return nil, err
	}

	return upload, nil
}

func (store ShardedFileStore) GetUpload(ctx context.Context, id string) (handler.Upload, error) {
	infoPath := store.infoPath(id)
	data, err := os.ReadFile(infoPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Interpret os.ErrNotExist as 404 Not Found
			err = handler.ErrNotFound
		}
		return nil, err
	}
	var info handler.FileInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}

	if info.Storage == nil || info.Storage["Path"] == "" {
		return nil, handler.ErrNotFound
	}
	binPath := info.Storage["Path"]

	stat, err := os.Stat(binPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Interpret os.ErrNotExist as 404 Not Found
			err = handler.ErrNotFound
		}
		return nil, err
	}

	info.Offset = stat.Size()

	return &fileUpload{
		store:    &store,
		info:     info,
		binPath:  binPath,
		infoPath: infoPath,
	}, nil
}

func (store ShardedFileStore) AsTerminatableUpload(upload handler.Upload) handler.TerminatableUpload {
	return upload.(*fileUpload)
}

func (store ShardedFileStore) AsLengthDeclarableUpload(upload handler.Upload) handler.LengthDeclarableUpload {
	return upload.(*fileUpload)
}

func (store ShardedFileStore) AsConcatableUpload(upload handler.Upload) handler.ConcatableUpload {
	return upload.(*fileUpload)
}

// defaultBinPath returns the path to the file storing the binary data, if it is
// not customized using the pre-create hook.
func (store ShardedFileStore) defaultBinPath(id string) string {
	return filepath.Join(store.BasePath, id)
}

// infoPath returns the path to the .info file storing the file's info.
func (store ShardedFileStore) infoPath(id string) string {
	return store.getShardedPath("meta", id, "info")
}

type fileUpload struct {
	store *ShardedFileStore
	// info stores the current information about the upload
	info handler.FileInfo
	// infoPath is the path to the .info file
	infoPath string
	// binPath is the path to the binary file (which has no extension)
	binPath string
}

func (upload *fileUpload) GetInfo(ctx context.Context) (handler.FileInfo, error) {
	return upload.info, nil
}

func (upload *fileUpload) WriteChunk(ctx context.Context, offset int64, src io.Reader) (int64, error) {
	file, err := os.OpenFile(upload.binPath, os.O_WRONLY|os.O_APPEND, defaultFilePerm)
	if err != nil {
		return 0, err
	}
	// Avoid the use of defer file.Close() here to ensure no errors are lost
	// See https://github.com/tus/tusd/issues/698.

	n, err := io.Copy(file, src)
	upload.info.Offset += n
	if err != nil {
		file.Close()
		return n, err
	}

	return n, file.Close()
}

func (upload *fileUpload) GetReader(ctx context.Context) (io.ReadCloser, error) {
	return os.Open(upload.binPath)
}

func (upload *fileUpload) Terminate(ctx context.Context) error {
	if err := upload.store.removeWithDirs(upload.infoPath); err != nil {
		upload.store.log.Error().
			Str("event", "upload_removed").
			Str("binPath", upload.infoPath).
			Err(err).
			Msg("Upload failed removal")
		return err
	}
	if err := upload.store.dBConn.TerminateUpload(upload.info.ID, time.Now().Unix()); err != nil {
		upload.store.log.Error().
			Str("event", "upload_removed").
			Str("binPath", upload.infoPath).
			Err(err).
			Msg("Upload failed db removal")
		return err
	}

	upload.store.log.Info().
		Str("event", "upload_removed").
		Str("binPath", upload.infoPath).
		Msg("Upload removed")

	duplicates, err := upload.store.dBConn.FetchDuplicateIds(upload.info.ID)
	if err != nil {
		return err
	}

	if duplicates == 0 {
		if err := upload.store.removeWithDirs(upload.binPath); err != nil {
			upload.store.log.Error().
				Str("event", "blob_deleted").
				Str("binPath", upload.binPath).
				Err(err).
				Msg("Upload data failed removal")
			return err
		}
		upload.store.log.Info().
			Str("event", "blob_deleted").
			Str("binPath", upload.binPath).
			Msg("Upload data removed")

	}

	return nil
}

func (upload *fileUpload) ConcatUploads(ctx context.Context, uploads []handler.Upload) (err error) {
	file, err := os.OpenFile(upload.binPath, os.O_WRONLY|os.O_APPEND, defaultFilePerm)
	if err != nil {
		return err
	}
	defer func() {
		// Ensure that close error is propagated, if it occurs.
		// See https://github.com/tus/tusd/issues/698.
		cerr := file.Close()
		if err == nil {
			err = cerr
		}
	}()

	for _, partialUpload := range uploads {
		fileUpload := partialUpload.(*fileUpload)

		src, err := os.Open(fileUpload.binPath)
		if err != nil {
			return err
		}

		if _, err := io.Copy(file, src); err != nil {
			return err
		}
	}

	return
}

func (upload *fileUpload) DeclareLength(ctx context.Context, length int64) error {
	upload.info.Size = length
	upload.info.SizeIsDeferred = false
	return upload.writeInfo()
}

// writeInfo updates the entire information. Everything will be overwritten.
func (upload *fileUpload) writeInfo() error {
	data, err := json.Marshal(upload.info)
	if err != nil {
		return err
	}
	return createFile(upload.infoPath, data)
}

func (upload *fileUpload) FinishUpload(ctx context.Context) error {
	upload.store.log.Debug().
		Str("event", "upload_finished").
		Str("id", upload.info.ID).Msg("Finishing upload")

	// Calculate file hash
	hashBytes, err := upload.getHash()
	if err != nil {
		upload.store.log.Error().
			Err(err).
			Msg("Failed to hash completed upload")
		return err
	}
	hashString := fmt.Sprintf("%x", hashBytes)

	// Update file size
	stat, err := os.Stat(upload.binPath)
	if err != nil {
		upload.store.log.Error().
			Err(err).
			Msg("Failed to stat completed upload")
		return err
	}
	size := stat.Size()
	upload.info.Offset = size
	upload.info.Size = size

	var expires int64
	if upload.info.MetaData["identified"] == "" {
		expires = utils.DurationToExpire(upload.store.ExpireTime)
	} else {
		expires = utils.DurationToExpire(upload.store.ExpireIdentifiedTime)
	}
	upload.info.MetaData["expires"] = strconv.FormatInt(expires, 10)

	if err := upload.store.dBConn.FinishUpload(upload.info.ID, hashBytes, expires, size); err != nil {
		upload.store.log.Error().
			Err(err).
			Msg("Failed to update db")
		return err
	}

	oldPath := upload.binPath
	newPath := upload.store.getShardedPath("complete", hashString, "bin")

	if err := os.MkdirAll(filepath.Dir(newPath), defaultDirectoryPerm); err != nil {
		upload.store.log.Error().
			Err(err).
			Msg("Failed to create complete path")
		return err
	}

	if _, err := os.Stat(newPath); err != nil {
		// File needs moving to the sharded directory
		if err := os.Rename(oldPath, newPath); err != nil {
			upload.store.log.Error().
				Err(err).
				Str("oldPath", oldPath).
				Str("newPath", newPath).
				Msg("Failed to rename")
			return err
		}
	} else {
		// File already exists just remove the temporary upload
		if err = os.Remove(oldPath); err != nil {
			upload.store.log.Error().
				Err(err).
				Str("oldPath", oldPath).
				Msg("Failed to remove")
		}
	}

	upload.info.Storage["Path"] = newPath

	expired, err := upload.store.dBConn.FetchExpiredIds()
	if err != nil {
		upload.store.log.Error().
			Err(err).
			Msg("Failed to fetch expired id's")
		return err
	}

	spew.Dump(expired)

	return upload.writeInfo()
}

// createFile creates the file with the content. If the corresponding directory does not exist,
// it is created. If the file already exists, its content is removed.
func createFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, defaultFilePerm)
	if err != nil {
		if os.IsNotExist(err) {
			// An upload ID containing slashes is mapped onto different directories on disk,
			// for example, `myproject/uploadA` should be put into a folder called `myproject`.
			// If we get an error indicating that a directory is missing, we try to create it.
			if err := os.MkdirAll(filepath.Dir(path), defaultDirectoryPerm); err != nil {
				return fmt.Errorf("failed to create directory for %s: %s", path, err)
			}

			// Try creating the file again.
			file, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, defaultFilePerm)
			if err != nil {
				// If that still doesn't work, error out.
				return err
			}
		} else {
			return err
		}
	}

	if content != nil {
		if _, err := file.Write(content); err != nil {
			return err
		}
	}

	return file.Close()
}

// ADDED FUNCTIONS

// generates a directory hierarchy
func (store *ShardedFileStore) shards(id string) string {
	if len(id) < store.ShardLayers {
		panic("id is too short for requested number of shard layers")
	}
	shards := make([]string, store.ShardLayers)
	for idx, char := range id[:store.ShardLayers] {
		shards[idx] = string(char)
	}
	return filepath.Join(shards...)
}

func (store *ShardedFileStore) getDirectory(dir string) string {
	return filepath.Join(store.BasePath, dir)
}

func (store *ShardedFileStore) getPath(dir string, id string, ext string) string {
	return filepath.Join(store.BasePath, dir, id+"."+ext)
}

func (store *ShardedFileStore) getShardedPath(dir string, id string, ext string) string {
	shards := store.shards(id)
	return filepath.Join(store.BasePath, dir, shards, id+"."+ext)
}

func (store *ShardedFileStore) removeWithDirs(path string) error {
	absBase, err := filepath.Abs(store.BasePath)
	if err != nil {
		return err
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}

	if !strings.HasPrefix(absPath, absBase) {
		return fmt.Errorf("path %#v is not prefixed by basepath %#v", path, store.BasePath)
	}

	if err := os.Remove(absPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	parent := path
	for {
		parent = filepath.Dir(parent)
		parentAbs, err := filepath.Abs(parent)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(parentAbs, absBase) || parentAbs == absBase {
			break
		}

		empty, err := utils.IsDirEmpty(parent)
		if empty {
			err = os.Remove(parent)
		}
		if err != nil {
			return err
		}
	}

	return nil
}

func (upload *fileUpload) getHash() ([]byte, error) {
	file, err := os.Open(upload.binPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return nil, err
	}

	return hash.Sum(nil), nil
}
