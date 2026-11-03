package api

import (
	"github.com/gin-gonic/gin"

	"nixmox/internal/cluster"
	"nixmox/internal/domain"
)

func registerTaskRoutes(clustered *gin.RouterGroup) {
	clustered.GET("/tasks", func(c *gin.Context) {
		list(c, services(c).Tasks.Tasks)
	})
	clustered.GET("/tasks/:id", func(c *gin.Context) {
		record, err := services(c).Tasks.Tasks.Get(c.Request.Context(), c.Param("id"))
		respondRecord(c, record, err)
	})
	clustered.POST("/tasks/:id/cancel", func(c *gin.Context) {
		if err := services(c).Tasks.Cancel(c.Request.Context(), c.Param("id")); err != nil {
			fail(c, err)
			return
		}
		c.Status(202)
	})
	clustered.POST("/tasks/:id/retry", func(c *gin.Context) {
		result, err := services(c).Tasks.Retry(c.Request.Context(), c.Param("id"))
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(202, result)
	})
	clustered.POST("/tasks/:id/approve", func(c *gin.Context) {
		var input struct {
			OutputID string `json:"outputId"`
		}
		if !body(c, &input) {
			return
		}
		if err := services(c).Tasks.Approve(c.Request.Context(), c.Param("id"), input.OutputID); err != nil {
			fail(c, err)
			return
		}
		c.Status(202)
	})
	clustered.GET("/tasks/:id/outputs/:output", func(c *gin.Context) {
		record, err := services(c).Tasks.Outputs.Get(c.Request.Context(), c.Param("output"))
		if err == nil && record.Value.TaskID != c.Param("id") {
			c.Status(404)
			return
		}
		respondRecord(c, record, err)
	})
	clustered.GET("/tasks/:id/events", func(c *gin.Context) {
		id := c.Param("id")
		store := services(c).Tasks.Tasks
		if _, err := store.Get(c.Request.Context(), id); err != nil {
			fail(c, err)
			return
		}
		sse(c, func(send func(string, uint64, any) error) error {
			return store.Watch(c.Request.Context(), id, func(record *cluster.Record[domain.Task], deleted bool) error {
				if record == nil || deleted {
					return nil
				}
				return send("task", record.Revision, record.Value)
			})
		})
	})
}
