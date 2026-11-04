package api

import (
	"github.com/gin-gonic/gin"
	"nixmox/internal/deployment"
)

func registerDeploymentRoutes(routes *gin.RouterGroup) {
	routes.PUT("/hosts/:id/configuration", func(c *gin.Context) {
		svc := services(c).Deployments
		if svc == nil {
			c.Status(503)
			return
		}
		var input deployment.Input
		if !body(c, &input) {
			return
		}
		result, err := svc.Assign(c.Request.Context(), c.Param("id"), input)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, result)
	})
	routes.POST("/hosts/:id/deployments", func(c *gin.Context) {
		svc := services(c).Deployments
		if svc == nil {
			c.Status(503)
			return
		}
		var input deployment.Input
		if !body(c, &input) {
			return
		}
		result, err := svc.Submit(c.Request.Context(), c.Param("id"), input)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(202, result)
	})
}
