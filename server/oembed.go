package server

import (
	"net/http"

	"github.com/davecgh/go-spew/spew"
	"github.com/gin-gonic/gin"
	"github.com/kiwiirc/plugin-fileuploader/shardedfilestore"
)

type response struct {
	Type            string `json:"type"`
	Version         string `json:"version"`
	Title           string `json:"title"`
	AuthorName      string `json:"author_name"`
	ThumbnailUrl    string `json:"thumbnail_url"`
	ThumbnailWidth  string `json:"thumbnail_width"`
	ThumbnailHeight string `json:"thumbnail_height"`
}

func (serv *UploadServer) registerOembedHandlers(r *gin.Engine, store *shardedfilestore.ShardedFileStore) error {
	rg := r.Group("/oembed/")
	rg.GET(":id", handleOembed)
	rg.GET(":id/:filename", handleOembed)
	return nil
}

func handleOembed(c *gin.Context) {
	spew.Dump(c.Param("id"))

	c.JSON(http.StatusOK, response{
		Type:         "test",
		Version:      "1.0",
		Title:        "User supplied image",
		ThumbnailUrl: "http://some.url",
	})
}
