package gateway

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"nixmox/internal/domain"
	"nixmox/internal/service"
)

// Task watches reuse authoritative reads, including remote-owner routing.
func watchTask(c *gin.Context, s *service.Service) {
	id := domain.ID(c.Query("task_id"))
	logs := c.Query("topics") == "task.logs"
	offset, err := strconv.ParseInt(c.DefaultQuery("offset", "0"), 10, 64)
	if id == "" || err != nil || offset < 0 || (!logs && offset != 0) || !queries(c, "topics", "task_id", "offset") {
		failure(c, &domain.Error{Code: "INVALID_INPUT", Message: "Invalid task watch selector or offset"}, nil)
		return
	}
	ctx := c.Request.Context()
	controller := http.NewResponseController(c.Writer)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		var value any
		var complete bool
		event := "task.snapshot"
		if logs {
			var chunk domain.LogChunk
			chunk, err = s.Logs(ctx, id, offset, domain.LogReadDefault)
			value, complete, offset = chunk, chunk.Complete && chunk.DataBase64 == "", chunk.NextOffset
			event = "task.log"
		} else {
			var data json.RawMessage
			data, err = s.Read(ctx, service.ReadRequest{Collection: "tasks", ID: &id})
			var task domain.Task
			if err == nil {
				err = json.Unmarshal(data, &task)
			}
			value = task
			complete = task.State == domain.Succeeded || task.State == domain.Failed || task.State == domain.Cancelled
		}
		if err != nil {
			if !c.Writer.Written() {
				failure(c, err, nil)
			}
			return
		}
		c.Header("Content-Type", "text/event-stream")
		c.Header("X-Accel-Buffering", "no")
		if controller.SetWriteDeadline(time.Now().Add(5*time.Second)) != nil {
			return
		}
		c.SSEvent(event, value)
		if complete {
			c.SSEvent("done", struct{}{})
		}
		if controller.Flush() != nil || complete {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
