package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/nats.go/jetstream"

	"nixmox/internal/cluster"
	"nixmox/internal/recipe"
)

func requestLimit(c *gin.Context) (int, bool) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if err != nil || limit < 1 || limit > 500 {
		c.Status(http.StatusBadRequest)
		return 0, false
	}
	return limit, true
}

func requestVersion(c *gin.Context) (uint64, bool) {
	version, err := strconv.ParseUint(c.Param("version"), 10, 64)
	if err != nil || version == 0 {
		c.Status(400)
		return 0, false
	}
	return version, true
}

func services(c *gin.Context) *Services { return c.MustGet("services").(*Services) }

func body(c *gin.Context, destination any) bool {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		c.AbortWithStatusJSON(400, gin.H{"error": "invalid JSON request"})
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		c.AbortWithStatusJSON(400, gin.H{"error": "request must contain one JSON value"})
		return false
	}
	return true
}

func fail(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := "operation failed"
	if errors.Is(err, recipe.ErrInvalid) {
		status = http.StatusBadRequest
		message = err.Error()
	}
	if errors.Is(err, jetstream.ErrKeyNotFound) || errors.Is(err, jetstream.ErrKeyDeleted) {
		status = 404
		message = "record not found"
	}
	if errors.Is(err, jetstream.ErrKeyExists) {
		status = 409
		message = "record changed; refresh and retry"
	}
	c.JSON(status, gin.H{"error": message})
}

func respondRecord[T any](c *gin.Context, record cluster.Record[T], err error) {
	if err != nil {
		fail(c, err)
		return
	}
	c.Header("ETag", `"`+strconv.FormatUint(record.Revision, 10)+`"`)
	c.JSON(200, record.Value)
}

func list[T any](c *gin.Context, store cluster.Store[T]) {
	limit, ok := requestLimit(c)
	if !ok {
		return
	}
	records, next, err := store.List(c.Request.Context(), c.Query("cursor"), limit)
	if err != nil {
		fail(c, err)
		return
	}
	items := make([]T, 0, len(records))
	for _, record := range records {
		items = append(items, record.Value)
	}
	c.JSON(200, gin.H{"items": items, "nextCursor": next})
}

func sse(c *gin.Context, stream func(func(string, uint64, any) error) error) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	controller := http.NewResponseController(c.Writer)
	err := stream(func(event string, id uint64, value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if _, err = io.WriteString(c.Writer, "id: "+strconv.FormatUint(id, 10)+"\nevent: "+event+"\ndata: "+string(data)+"\n\n"); err != nil {
			return err
		}
		return controller.Flush()
	})
	if err != nil && c.Request.Context().Err() == nil && !c.Writer.Written() {
		c.Header("Content-Type", "application/json")
		fail(c, err)
	}
}
