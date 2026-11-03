package api

import (
	"github.com/gin-gonic/gin"
	"nixmox/internal/enrollment"
)

func (s Server) registerEnrollmentRoutes(router *gin.Engine, clustered *gin.RouterGroup) {
	router.POST("/internal/enrollment/add", func(c *gin.Context) {
		if s.InviteEnrollment == nil {
			c.Status(503)
			return
		}
		var input enrollment.Request
		if !body(c, &input) {
			return
		}
		result, err := s.InviteEnrollment(c.Request.Context(), input)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, result)
	})
	router.POST("/internal/enrollment/confirm", func(c *gin.Context) {
		if s.ConfirmEnrollment == nil {
			c.Status(503)
			return
		}
		var input enrollment.Confirmation
		if !body(c, &input) {
			return
		}
		if err := s.ConfirmEnrollment(c.Request.Context(), input); err != nil {
			fail(c, err)
			return
		}
		c.Status(204)
	})
	routes := clustered.Group("/enrollment")
	routes.Use(func(c *gin.Context) {
		if services(c).Enrollment == nil {
			c.AbortWithStatus(503)
			return
		}
		c.Next()
	})
	routes.POST("/request", func(c *gin.Context) {
		var input enrollment.Input
		if !body(c, &input) {
			return
		}
		result, err := services(c).Enrollment.Submit(c.Request.Context(), input)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, result)
	})
	routes.GET("", func(c *gin.Context) { list(c, services(c).Enrollment.Requests) })
	routes.GET("/:id", func(c *gin.Context) {
		record, err := services(c).Enrollment.Requests.Get(c.Request.Context(), c.Param("id"))
		respondRecord(c, record, err)
	})
}
