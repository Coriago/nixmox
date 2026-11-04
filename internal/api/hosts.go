package api

import (
	"encoding/json"

	"github.com/gin-gonic/gin"

	"nixmox/internal/domain"
	"nixmox/internal/host"
	"nixmox/internal/membership"
)

func registerHostRoutes(clustered *gin.RouterGroup) {
	clustered.POST("/hosts/:id/promote", func(c *gin.Context) {
		service := services(c).Membership
		if service == nil {
			c.Status(503)
			return
		}
		var input membership.Input
		if !body(c, &input) {
			return
		}
		input.NodeID = c.Param("id")
		result, err := service.Submit(c.Request.Context(), input)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(202, result)
	})
	clustered.GET("/hosts/:id/disks", func(c *gin.Context) {
		collect := services(c).HostDisks
		if collect == nil {
			c.Status(503)
			return
		}
		result, err := collect(c.Request.Context(), c.Param("id"))
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, result)
	})
	clustered.GET("/hosts/:id/metrics/stream", func(c *gin.Context) {
		svc := services(c)
		if svc.Metrics.Connection == nil {
			c.Status(503)
			return
		}
		if _, err := svc.Members.Get(c.Request.Context(), c.Param("id")); err != nil {
			fail(c, err)
			return
		}
		sse(c, func(send func(string, uint64, any) error) error {
			return svc.Metrics.Follow(c.Request.Context(), c.Param("id"), func(data json.RawMessage) error { return send("metrics", 0, data) })
		})
	})
	clustered.GET("/hosts", func(c *gin.Context) {
		list(c, services(c).Members)
	})
	clustered.GET("/hosts/:id", func(c *gin.Context) {
		record, err := services(c).Members.Get(c.Request.Context(), c.Param("id"))
		respondRecord(c, record, err)
	})
	clustered.POST("/hosts/:id/builds", func(c *gin.Context) {
		manager := services(c).Tasks
		if c.Param("id") != manager.HostID {
			c.JSON(503, gin.H{"error": "remote host dispatch is not available"})
			return
		}
		var input host.BuildInput
		if !body(c, &input) {
			return
		}
		if err := input.TargetRepo.Validate(); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		data, err := json.Marshal(input)
		if err != nil {
			fail(c, err)
			return
		}
		result, err := manager.Submit(c.Request.Context(), domain.Task{HostID: manager.HostID, Type: "build", Inputs: data})
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(202, result)
	})
}
