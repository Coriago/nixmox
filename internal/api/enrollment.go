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
	clustered.POST("/internal/enrollment/request", func(c *gin.Context) {
		svc := services(c).Enrollment
		if svc == nil {
			c.Status(503)
			return
		}
		var input enrollment.JoiningNode
		if !body(c, &input) {
			return
		}
		result, err := svc.Receive(c.Request.Context(), input)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, result)
	})
	routes := clustered.Group("/enrollment")
	routes.Use(func(c *gin.Context) {
		if services(c).Enrollment == nil {
			c.AbortWithStatus(503)
			return
		}
		c.Next()
	})
	router.POST("/enrollment/request", func(c *gin.Context) {
		var input enrollment.Input
		if !body(c, &input) {
			return
		}
		submit := s.RequestEnrollment
		if s.Services != nil {
			if svc, err := s.Services(); err == nil && svc != nil {
				if svc.Pending {
					c.Status(403)
					return
				}
				if svc.Enrollment != nil {
					submit = svc.Enrollment.Submit
				}
			}
		}
		if submit == nil {
			c.Status(503)
			return
		}
		result, err := submit(c.Request.Context(), input)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, result)
	})
	for action, decision := range map[string]string{"approve": "approved", "reject": "rejected"} {
		routes.POST("/:id/"+action, func(c *gin.Context) {
			result, err := services(c).Enrollment.Decide(c.Request.Context(), c.Param("id"), decision)
			if err != nil {
				fail(c, err)
				return
			}
			c.JSON(200, result)
		})
	}
	routes.GET("", func(c *gin.Context) { list(c, services(c).Enrollment.Requests) })
	router.GET("/enrollment/:id", func(c *gin.Context) {
		if s.Services != nil {
			if svc, err := s.Services(); err == nil && svc != nil && !svc.Pending && svc.Enrollment != nil {
				record, err := svc.Enrollment.Requests.Get(c.Request.Context(), c.Param("id"))
				respondRecord(c, record, err)
				return
			}
		}
		if s.EnrollmentStatus == nil {
			c.Status(503)
			return
		}
		result, err := s.EnrollmentStatus(c.Request.Context(), c.Param("id"))
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, result)
	})
}
