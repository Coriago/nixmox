// Package gateway adapts the HTTP API to services; it never executes host effects.
package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"

	"nixmox/internal/domain"
	"nixmox/internal/service"
)

func New(s *service.Service) http.Handler {
	router := gin.New()
	router.Use(otelgin.Middleware("nixmox"))
	_ = router.SetTrustedProxies(nil)
	// Do not log request bodies, headers, credentials or panic values.
	router.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, recovered any) {
		if recovered == http.ErrAbortHandler {
			panic(recovered)
		}
		failure(c, errors.New("handler panic"), nil)
	}))
	router.Use(func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Cache-Control", "no-store")
		c.Next()
	})
	cacheRoutes(router, s)
	api := router.Group("/api/v1")
	enrollment(api, s)
	api.POST("/commands", func(c *gin.Context) {
		media, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if err != nil || media != "application/json" {
			c.AbortWithStatusJSON(http.StatusUnsupportedMediaType, domain.ErrorResponse{Error: &domain.Error{Code: "INVALID_INPUT", Message: "Content-Type must be application/json"}})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, domain.CommandPayloadLimit+16<<10)
		decoder := json.NewDecoder(c.Request.Body)
		decoder.DisallowUnknownFields()
		var command domain.Command
		if err = decoder.Decode(&command); err != nil {
			failure(c, &domain.Error{Code: "INVALID_INPUT", Message: "Invalid command JSON"}, nil)
			return
		}
		if decoder.Decode(new(any)) != io.EOF {
			failure(c, &domain.Error{Code: "INVALID_INPUT", Message: "Expected one command"}, &command.ID)
			return
		}
		receipt, err := s.Submit(c.Request.Context(), command)
		if err != nil {
			failure(c, err, &command.ID)
			return
		}
		c.JSON(http.StatusAccepted, receipt)
	})
	api.GET("/commands/:id", func(c *gin.Context) {
		id := domain.ID(c.Param("id"))
		receipt, err := s.Receipt(c.Request.Context(), id)
		if err != nil {
			failure(c, err, &id)
			return
		}
		c.JSON(http.StatusOK, receipt)
	})
	for _, path := range []string{"identity", "cluster/status", "cluster/requests", "network/status", "hosts", "tasks", "discoveries", "repositories", "configurations", "cache/status", "cache/locations"} {
		api.GET("/"+path, read(s, path, ""))
	}
	for _, collection := range []string{"repositories", "configurations"} {
		api.GET("/"+collection+"/:id", read(s, collection, ""))
	}
	api.GET("/repositories/:id/refs", read(s, "repositories", "refs"))
	api.GET("/hosts/:id", read(s, "hosts", ""))
	api.GET("/hosts/:id/telemetry", read(s, "hosts", "telemetry"))
	api.GET("/tasks/:id", read(s, "tasks", ""))
	api.GET("/tasks/:id/logs", func(c *gin.Context) {
		offset, err := strconv.ParseInt(c.DefaultQuery("offset", "0"), 10, 64)
		limit, limitErr := strconv.Atoi(c.DefaultQuery("limit", strconv.Itoa(domain.LogReadDefault)))
		if err != nil || limitErr != nil || offset < 0 || limit < 1 || limit > domain.LogReadLimit || !queries(c, "offset", "limit") {
			failure(c, &domain.Error{Code: "INVALID_INPUT", Message: "Invalid log bounds"}, nil)
			return
		}
		chunk, err := s.Logs(c.Request.Context(), domain.ID(c.Param("id")), offset, limit)
		if err != nil {
			failure(c, err, nil)
			return
		}
		c.JSON(http.StatusOK, chunk)
	})
	api.GET("/events", func(c *gin.Context) {
		if c.Query("topics") == "cache.status" {
			if !queries(c, "topics") {
				failure(c, &domain.Error{Code: "INVALID_INPUT", Message: "Invalid cache event selector"}, nil)
				return
			}
			watchCache(c, s)
			return
		}
		if c.Query("topics") == "tasks" || c.Query("topics") == "task.logs" {
			watchTask(c, s)
			return
		}
		if c.Query("topics") != "hosts.telemetry" || c.Query("host_id") == "" || !queries(c, "topics", "host_id") {
			failure(c, &domain.Error{Code: "INVALID_INPUT", Message: "This node supports hosts.telemetry events with host_id"}, nil)
			return
		}
		sub, err := s.Telemetry(c.Request.Context(), domain.ID(c.Query("host_id")))
		if err != nil {
			failure(c, err, nil)
			return
		}
		defer sub.Close()
		c.Header("Content-Type", "text/event-stream")
		c.Header("X-Accel-Buffering", "no")
		controller := http.NewResponseController(c.Writer)
		for {
			sample, err := sub.Next(c.Request.Context())
			if err != nil {
				return
			}
			if err = controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
				return
			}
			c.SSEvent("host.telemetry", sample)
			if err = controller.Flush(); err != nil {
				return
			}
		}
	})
	router.NoRoute(func(c *gin.Context) { failure(c, &domain.Error{Code: "NOT_FOUND", Message: "Route not found"}, nil) })
	return http.NewCrossOriginProtection().Handler(router)
}

func read(s *service.Service, collection, subresource string) gin.HandlerFunc {
	return func(c *gin.Context) {
		values, err := url.ParseQuery(c.Request.URL.RawQuery)
		if err != nil {
			failure(c, &domain.Error{Code: "INVALID_INPUT", Message: "Invalid query encoding"}, nil)
			return
		}
		page, err := service.ParsePageQuery(values)
		if err != nil {
			failure(c, err, nil)
			return
		}
		request := service.ReadRequest{Collection: collection, Subresource: subresource, Page: page}
		if value := c.Param("id"); value != "" {
			id := domain.ID(value)
			request.ID = &id
		}
		value, err := s.Read(c.Request.Context(), request)
		if err != nil {
			failure(c, err, nil)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", value)
	}
}

func queries(c *gin.Context, allowed ...string) bool {
	for key, values := range c.Request.URL.Query() {
		found := false
		for _, name := range allowed {
			found = found || key == name
		}
		if !found || len(values) != 1 {
			return false
		}
	}
	return true
}

func failure(c *gin.Context, err error, command *domain.ID) {
	var detail *domain.Error
	status := http.StatusInternalServerError
	if errors.Is(err, os.ErrNotExist) {
		detail = &domain.Error{Code: "NOT_FOUND", Message: "Resource not found"}
	} else if !errors.As(err, &detail) {
		detail = &domain.Error{Code: "INTERNAL", Message: "Request failed; inspect daemon diagnostics"}
	}
	switch detail.Code {
	case "INVALID_INPUT":
		status = http.StatusBadRequest
	case "CANCELLATION_REFUSED", "CONFIRMATION_REQUIRED":
		status = http.StatusUnprocessableEntity
	case "REQUEST_LIMIT":
		status = http.StatusTooManyRequests
	case "NOT_FOUND":
		status = http.StatusNotFound
	case "CURSOR_EXPIRED", "COMMAND_EXPIRED":
		status = http.StatusGone
	case "RANGE_NOT_SATISFIABLE":
		status = http.StatusRequestedRangeNotSatisfiable
	case "VERSION_CONFLICT", "IDEMPOTENCY_CONFLICT", "CONFIGURATION_CONFLICT":
		status = http.StatusConflict
	case "UNSUPPORTED_CAPABILITY", "TARGET_UNREACHABLE", "QUORUM_UNAVAILABLE", "SUBMISSION_UNKNOWN", "SOURCE_UNAVAILABLE", "CREDENTIAL_UNAVAILABLE", "CATALOG_UNAVAILABLE", "CONFIGURATION_UNAVAILABLE", "CACHE_UNAVAILABLE":
		status = http.StatusServiceUnavailable
	}
	// Only expose the sanitized domain error to instrumentation, never the
	// underlying error (which may contain an untrusted body or credential).
	_ = c.Error(detail)
	c.AbortWithStatusJSON(status, domain.ErrorResponse{Error: detail, CommandID: command})
}
