package server

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/exp/slog"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	tusd "github.com/tus/tusd/v2/pkg/handler"

	"github.com/kiwiirc/plugin-fileuploader/events"
	"github.com/kiwiirc/plugin-fileuploader/logging"
	"github.com/kiwiirc/plugin-fileuploader/shardedfilestore"
	"github.com/kiwiirc/plugin-fileuploader/utils"
)

func customizedCors(serv *UploadServer) gin.HandlerFunc {
	// convert slice values to keys of map for "contains" test
	originSet := make(map[string]struct{}, len(serv.cfg.Server.CorsOrigins))
	allowAll := false
	exists := struct{}{}
	for _, origin := range serv.cfg.Server.CorsOrigins {
		if origin == "*" {
			allowAll = true
			continue
		}
		originSet[origin] = exists
	}

	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		respHeader := c.Writer.Header()

		// only allow the origin if it's in the list from the config
		if allowAll && origin != "" {
			respHeader.Set("Access-Control-Allow-Origin", origin)
		} else if _, ok := originSet[origin]; ok {
			respHeader.Set("Access-Control-Allow-Origin", origin)
		} else {
			respHeader.Del("Access-Control-Allow-Origin")
			if c.Request.Method != "HEAD" && c.Request.Method != "GET" {
				// Don't log unknown cors origin for HEAD or GET requests
				serv.log.Warn().Str("origin", origin).Msg("Unknown cors origin")
			}
		}

		// lets the user-agent know the response can vary depending on the origin of the request.
		// ensures correct behaviour of browser cache.
		respHeader.Add("Vary", "Origin")
	}
}

func (serv *UploadServer) fileuploaderMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != "POST" && c.Request.Method != "DELETE" {
			// Metadata is only required for POST and DELETE requests
			return
		}

		// Determine the originating IP
		c.Request.Header.Del("K-Remote-IP")
		remoteIP, err := serv.getDirectOrForwardedRemoteIP(c.Request)
		if err != nil {
			if addrErr, ok := err.(*net.AddrError); ok {
				c.AbortWithError(http.StatusInternalServerError, addrErr).SetType(gin.ErrorTypePrivate)
			} else {
				c.AbortWithError(http.StatusNotAcceptable, err)
			}
			return
		}
		c.Request.Header.Set("K-Remote-IP", remoteIP)

		if authHeader, err := serv.processJwt(c); err != nil {
			// Jwt failures are none fatal, but will result in the uploaded being treated as anonymous
			// Stick a warning in the log to help with debugging
			serv.log.Warn().
				Err(err).
				Str("extjwt", authHeader).
				Msg("Failed to process EXTJWT")
		}
	}
}

func (serv *UploadServer) registerTusHandlers(r *gin.Engine, store *shardedfilestore.ShardedFileStore) (*tusd.UnroutedHandler, error) {
	maximumUploadSize := serv.cfg.Storage.MaximumUploadSize
	serv.log.Debug().Str("size", maximumUploadSize.String()).Msg("Using upload limit")

	config := tusd.Config{
		BasePath:                  serv.cfg.Server.BasePath,
		StoreComposer:             serv.composer,
		MaxSize:                   int64(maximumUploadSize.Bytes()),
		Logger:                    slog.New(slog.NewTextHandler(io.Discard, nil)),
		NotifyCompleteUploads:     true,
		NotifyCreatedUploads:      true,
		NotifyTerminatedUploads:   true,
		NotifyUploadProgress:      true,
		PreUploadCreateCallback:   serv.preUploadCreate,
		PreFinishResponseCallback: serv.preFinishResponse,
	}

	routePrefix, err := routePrefixFromBasePath(serv.cfg.Server.BasePath)
	if err != nil {
		return nil, err
	}

	handler, err := tusd.NewUnroutedHandler(config)
	if err != nil {
		return nil, err
	}

	// create event broadcaster
	serv.tusEventBroadcaster = events.NewTusEventBroadcaster(handler)

	// attach logger
	go logging.TusdLogger(serv.log, serv.tusEventBroadcaster)

	noopHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	tusdMiddleware := gin.WrapH(handler.Middleware(noopHandler))

	rg := r.Group(routePrefix)
	rg.Use(tusdMiddleware)
	rg.Use(customizedCors(serv))
	rg.Use(serv.fileuploaderMiddleware())

	// postFile := gin.WrapF(handler.PostFile)
	rg.POST("", serv.postFile(handler))

	// Register a dummy handler for OPTIONS, without this the middleware's would not be called
	rg.OPTIONS("*any", gin.WrapH(noopHandler))

	headFile := rewritePath(gin.WrapF(handler.HeadFile))
	rg.HEAD(":id", headFile)
	rg.HEAD(":id/:filename", headFile)

	// getFile := serv.getFile(handler, store)
	getFile := rewritePath(gin.WrapF(handler.GetFile))
	rg.GET(":id", getFile)
	rg.GET(":id/:filename", getFile)

	patchFile := rewritePath(serv.patchFile(handler))
	rg.PATCH(":id", patchFile)
	rg.PATCH(":id/:filename", patchFile)

	// Only attach the DELETE handler if the Terminate() method is provided
	if serv.composer.UsesTerminater {
		delFile := rewritePath(serv.delFile(handler))
		rg.DELETE(":id", delFile)
		rg.DELETE(":id/:filename", delFile)
	}

	return handler, nil
}

func (serv *UploadServer) patchFile(handler *tusd.UnroutedHandler) gin.HandlerFunc {
	return func(c *gin.Context) {
		fmt.Println("patch")
		handler.PatchFile(c.Writer, c.Request)
	}
}

func (serv *UploadServer) postFile(handler *tusd.UnroutedHandler) gin.HandlerFunc {
	return func(c *gin.Context) {
		jwtAccount := c.Request.Header.Get("K-Jwt-Account")

		if serv.cfg.Server.RequireJwtAccount {
			if jwtAccount == "" {
				c.Error(errors.New("missing JWT account")).SetType(gin.ErrorTypePublic)
				c.AbortWithStatusJSON(http.StatusUnauthorized, "Account required")
				return
			}
		}

		fmt.Println("create")

		handler.PostFile(c.Writer, c.Request)
	}
}

func (serv *UploadServer) delFile(handler *tusd.UnroutedHandler) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		result, err := serv.DBConn.FetchUpload(id)
		if err == sql.ErrNoRows {
			c.AbortWithStatus(http.StatusNotFound)
			return
		} else if err != nil {
			c.AbortWithError(http.StatusInternalServerError, err).SetType(gin.ErrorTypePrivate)
			return
		}

		jwtAccount := c.Request.Header.Get("K-Jwt-Account")
		jwtIssuer := c.Request.Header.Get("K-Jwt-Issuer")
		remoteIP := c.Request.Header.Get("K-Remote-IP")

		if result["jwt_account"] != "" && (result["jwt_account"] != jwtAccount || result["jwt_issuer"] != jwtIssuer) {
			// The upload was created by an identified account that does not match this requests account
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		} else if result["uploader_ip"] == "" || result["uploader_ip"] != remoteIP {
			// The upload was created by an anonymous user that does not match this requests ip address
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}

		handler.DelFile(c.Writer, c.Request)
	}
}

func (serv *UploadServer) getSecretForToken(token *jwt.Token) (interface{}, error) {
	// Don't forget to validate the alg is what you expect:
	if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
		return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, errors.New("failed to get claims")
	}

	issuer, ok := claims["iss"]
	if !ok {
		return nil, errors.New("issuer field 'iss' missing from JWT")
	}

	issuerStr, ok := issuer.(string)
	if !ok {
		return nil, errors.New("failed to coerce issuer to string")
	}

	secret, ok := serv.cfg.JwtSecretsByIssuer[issuerStr]
	if !ok {
		// Attempt to get fallback issuer
		secret, ok = serv.cfg.JwtSecretsByIssuer["*"]

		if !ok {
			return nil, fmt.Errorf("issuer %#v not configured", issuerStr)
		} else {
			serv.log.Warn().
				Msg(fmt.Sprintf("issuer %#v not configured, used fallback", issuerStr))
		}
	}

	return []byte(secret), nil
}

func (serv *UploadServer) processJwt(c *gin.Context) (string, error) {
	// Ensure headers are not polluted
	c.Request.Header.Del("K-Jwt-Nick")
	c.Request.Header.Del("K-Jwt-Issuer")
	c.Request.Header.Del("K-Jwt-Account")

	// Get Authorization header
	authHeader := c.Request.Header.Get("Authorization")
	if authHeader == "" {
		return "", errors.New("no authorization header")
	}

	// Parse Token
	token, err := jwt.Parse(authHeader, serv.getSecretForToken)
	if err != nil {
		return authHeader, err
	}

	// Ensure token is valid
	if !token.Valid {
		return authHeader, errors.New("invalid jwt")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return authHeader, errors.New("no jwt claims")
	}

	if nick, ok := claims["sub"].(string); ok {
		c.Request.Header.Set("K-Jwt-Nick", nick)
	}
	if issuer, ok := claims["iss"].(string); ok {
		c.Request.Header.Set("K-Jwt-Issuer", issuer)
	}
	if account, ok := claims["account"].(string); ok {
		c.Request.Header.Set("K-Jwt-Account", account)
	}

	return authHeader, nil
}

// ErrInvalidXForwardedFor occurs if the X-Forwarded-For header is trusted but invalid
var ErrInvalidXForwardedFor = errors.New("failed to parse IP from X-Forwarded-For header")

func (serv *UploadServer) getDirectOrForwardedRemoteIP(req *http.Request) (string, error) {
	// extract direct IP
	remoteIPStr, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		serv.log.Error().
			Err(err).
			Msg("could not split address into host and port")
		return "", err
	}

	remoteIP := net.ParseIP(remoteIPStr)
	if remoteIP == nil {
		err := errors.New("failed to parse remote ip")
		serv.log.Error().
			Err(err)
		return "", err
	}

	// use X-Forwarded-For header if direct IP is a trusted reverse proxy
	if forwardedFor := req.Header.Get("X-Forwarded-For"); forwardedFor != "" {
		if serv.remoteIPisTrusted(remoteIP) {
			// We do not check intermediary proxies against the whitelist.
			// If a trusted proxy is appending to and forwarding the value of the
			// header it is receiving, that is an implicit expression of trust
			// which we will honour transitively.

			// take the first comma delimited address
			// this is the original client address
			parts := strings.Split(forwardedFor, ",")
			forwardedForClient := strings.TrimSpace(parts[0])
			forwardedForIP := net.ParseIP(forwardedForClient)
			if forwardedForIP == nil {
				err := ErrInvalidXForwardedFor
				serv.log.Error().
					Err(err).
					Str("client", forwardedForClient).
					Str("remoteIP", remoteIP.String()).
					Msg("Couldn't use trusted X-Forwarded-For header")
				return "", err
			}
			serv.checkLocalIP(forwardedForIP)
			return forwardedForIP.String(), nil
		}
		serv.log.Warn().
			Str("X-Forwarded-For", forwardedFor).
			Str("remoteIP", remoteIP.String()).
			Msg("Untrusted remote attempted to override stored IP")
	}

	// otherwise use direct IP
	serv.checkLocalIP(remoteIP)
	return remoteIP.String(), nil
}

func (serv *UploadServer) checkLocalIP(remoteIP net.IP) {
	if !utils.IsRemoteIP(remoteIP) {
		serv.log.Warn().
			Str("remoteIP", remoteIP.String()).
			Msg("Remote IP is not within public range")
	}
}

func (serv *UploadServer) remoteIPisTrusted(remoteIP net.IP) bool {
	// check if remote IP is a trusted reverse proxy
	for _, trustedNet := range serv.cfg.Server.TrustedReverseProxyRanges {
		if trustedNet.Contains(remoteIP) {
			return true
		}
	}
	return false
}

func routePrefixFromBasePath(basePath string) (string, error) {
	url, err := url.Parse(basePath)
	if err != nil {
		return "", err
	}

	return url.Path, nil
}

func rewritePath(handler gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		// rewrite request path to ":id" route pattern
		c.Request.URL.Path = url.PathEscape(c.Param("id"))

		// call the normal handler
		handler(c)
	}
}
