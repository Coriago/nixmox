package api

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"nixmox/internal/cluster"
)

func registerLogRoutes(clustered *gin.RouterGroup) {
	for _, kind := range []string{"host", "task"} {
		prefix := "/" + kind + "s/:id/logs"
		clustered.GET(prefix, func(c *gin.Context) {
			after, err := strconv.ParseUint(c.DefaultQuery("after", "0"), 10, 64)
			if err != nil {
				c.Status(400)
				return
			}
			result, err := services(c).Logs.Read(c.Request.Context(), kind, c.Param("id"), after, 100)
			if err != nil {
				fail(c, err)
				return
			}
			c.JSON(200, result)
		})
		clustered.GET(prefix+"/stream", func(c *gin.Context) {
			cursor := c.GetHeader("Last-Event-ID")
			if cursor == "" {
				cursor = c.DefaultQuery("after", "0")
			}
			after, err := strconv.ParseUint(cursor, 10, 64)
			if err != nil {
				c.Status(400)
				return
			}
			sse(c, func(send func(string, uint64, any) error) error {
				sendEntry := func(entry cluster.LogEntry) error {
					return send("log", entry.Sequence, entry.Data)
				}
				return services(c).Logs.Follow(c.Request.Context(), kind, c.Param("id"), after, sendEntry)
			})
		})
	}
}
