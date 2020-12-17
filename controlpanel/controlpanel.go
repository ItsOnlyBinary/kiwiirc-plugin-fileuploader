package controlpanel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/kiwiirc/plugin-fileuploader/db"
	"github.com/kiwiirc/plugin-fileuploader/shardedfilestore"
	"github.com/kiwiirc/plugin-fileuploader/templates"
)

type ControlPanel struct {
	DBConn     *db.DatabaseConnection
	Router     *gin.Engine
	Store      *shardedfilestore.ShardedFileStore
	countStmt  *sqlx.NamedStmt
	selectStmt *sqlx.NamedStmt
	users      map[string]string
}

type Upload struct {
	Id          *string `json:"id"`
	Uploader_IP *string `json:"remote"`
	Created_at  *int64  `json:"created"`
	JWT_Account *string `json:"account"`
}

func New(router *gin.Engine, dBConn *db.DatabaseConnection, store *shardedfilestore.ShardedFileStore, users map[string]string) *ControlPanel {
	m := &ControlPanel{
		Router: router,
		DBConn: dBConn,
		Store:  store,
		users:  users,
	}

	m.initStatements()

	// Register our handlers
	cs := cookie.NewStore([]byte("secret"))
	rg := m.Router.Group("/admin")
	rg.Use(sessions.Sessions("session", cs))
	rg.GET("/", m.handlePage)
	rg.GET("", m.handlePage)
	rg.POST("login", m.handleLogin)

	// Everything after here requires auth
	rg.Use(m.authMiddleware)
	rg.POST("del", m.handleDel)
	rg.GET("get", m.handleGet)
	rg.GET("logout", m.handleLogout)

	return m
}

func (m *ControlPanel) authMiddleware(c *gin.Context) {
	session := sessions.Default(c)
	user := session.Get("user")
	if user == nil {
		// Abort the request with the appropriate error code
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	// Continue down the chain to handler etc
	c.Next()
}

func (m *ControlPanel) handlePage(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(templates.Get["controlpanel"]))
}

func (m *ControlPanel) handleGet(c *gin.Context) {
	filter := c.Query("filter")

	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil {
		page = 1
	}

	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "50"))
	if err != nil {
		perPage = 50
	}

	uploads, total, err := m.getUploads(filter, page, perPage)
	c.JSON(http.StatusOK, map[string]interface{}{
		"uploads": uploads,
		"pages":   total / perPage,
	})
}

func (m *ControlPanel) handleDel(c *gin.Context) {
	if c.Request.Body == nil {
		c.Status(http.StatusBadRequest)
		return
	}
	defer c.Request.Body.Close()

	body, err := ioutil.ReadAll(c.Request.Body)
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}

	data := make(map[string][]string)
	err = json.Unmarshal(body, &data)
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}

	failed := make([]string, 0)
	for _, id := range data["terminate"] {
		err := m.Store.Terminate(id)
		if err != nil {
			failed = append(failed, id)
		}
	}

	if len(failed) > 0 {
		c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to terminate id's: " + strings.Join(failed, ", "),
		})
	}

	c.Status(http.StatusOK)
}

func (m *ControlPanel) getUploads(filter string, page, perPage int) (uploads []Upload, total int, err error) {
	offset := perPage * (page - 1)
	params := map[string]interface{}{
		"offset":  offset,
		"perpage": perPage,
		"filter":  "%" + filter + "%",
	}
	switch m.DBConn.DBConfig.DriverName {
	case "sqlite3", "mysql":
		err = m.countStmt.Get(&total, params)
		err = m.selectStmt.Select(&uploads, params)
	default:
		panic("Unhandled database driver")
	}

	return
}

func (m *ControlPanel) handleLogin(c *gin.Context) {
	if c.Request.Body == nil {
		c.Status(http.StatusBadRequest)
		return
	}
	defer c.Request.Body.Close()

	body, err := ioutil.ReadAll(c.Request.Body)
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}

	data := make(map[string]string)
	err = json.Unmarshal(body, &data)
	if err != nil {
		c.Status(http.StatusBadRequest)
		return
	}

	username := data["username"]
	password := data["password"]
	passhash := getHash(password)

	if chkPass, ok := m.users[username]; !ok || passhash != chkPass {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authentication failed"})
		return
	}

	session := sessions.Default(c)
	session.Set("user", username)
	if err := session.Save(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save session"})
		return
	}
}

func (m *ControlPanel) handleLogout(c *gin.Context) {
	session := sessions.Default(c)
	session.Delete("user")
	if err := session.Save(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save session"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Successfully logged out"})
}

func (m *ControlPanel) initStatements() {
	countStmt, _ := m.DBConn.DB.PrepareNamed(`
		SELECT COUNT(*) FROM uploads
		WHERE deleted != 1
		AND (
			id LIKE :filter
			OR
			uploader_ip LIKE :filter
			OR
			jwt_account LIKE :filter
		)
		`,
	)
	m.countStmt = countStmt

	selectStmt, _ := m.DBConn.DB.PrepareNamed(`
		SELECT id, uploader_ip, created_at, jwt_account FROM uploads
		WHERE deleted != 1
		AND (
			id LIKE :filter
			OR
			uploader_ip LIKE :filter
			OR
			jwt_account LIKE :filter
		)
		ORDER BY created_at DESC
		LIMIT :offset, :perpage
		`,
	)
	m.selectStmt = selectStmt
}

func getHash(url string) string {
	hasher := sha256.New()
	hasher.Write([]byte(url))
	return hex.EncodeToString(hasher.Sum(nil))
}
