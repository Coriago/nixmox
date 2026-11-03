package api

import (
	"github.com/gin-gonic/gin"

	"nixmox/internal/installer"
)

func registerInstallerRoutes(clustered *gin.RouterGroup) {
	routes := clustered.Group("")
	routes.Use(func(c *gin.Context) {
		if services(c).Installers == nil {
			c.AbortWithStatusJSON(503, gin.H{"error": "installer service unavailable"})
			return
		}
		c.Next()
	})
	routes.GET("/installers", func(c *gin.Context) { list(c, services(c).Installers.Definitions) })
	routes.GET("/installers/:id", func(c *gin.Context) {
		record, err := services(c).Installers.Definitions.Get(c.Request.Context(), c.Param("id"))
		respondRecord(c, record, err)
	})
	routes.POST("/installers", func(c *gin.Context) {
		var input installer.Registration
		if !body(c, &input) {
			return
		}
		result, err := services(c).Installers.Register(c.Request.Context(), input)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(201, result)
	})
	routes.GET("/install-prepare", func(c *gin.Context) {
		c.JSON(200, gin.H{"items": installer.Preparations(), "nextCursor": ""})
	})
	routes.GET("/install-finalize", func(c *gin.Context) {
		c.JSON(200, gin.H{"items": installer.Finalizers(), "nextCursor": ""})
	})
	routes.POST("/hosts/:id/installer-checks", func(c *gin.Context) {
		var input installer.LaunchInput
		if !body(c, &input) {
			return
		}
		result, err := services(c).Installers.Checks(c.Request.Context(), c.Param("id"), input.InstallerID, input.PrepareID)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, result)
	})
	routes.POST("/hosts/:id/install-finalize-checks", func(c *gin.Context) {
		var input struct {
			Root       string `json:"root"`
			SystemPath string `json:"systemPath"`
			FinalizeID string `json:"finalizeId"`
		}
		if !body(c, &input) {
			return
		}
		if input.FinalizeID != "" && input.FinalizeID != "finalize-general" {
			c.JSON(400, gin.H{"error": "unknown finalizer"})
			return
		}
		result, err := services(c).Installers.FinalizeChecks(c.Request.Context(), c.Param("id"), input.Root, input.SystemPath)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, result)
	})
	routes.POST("/hosts/:id/install-finalize", func(c *gin.Context) {
		var input installer.FinalizeInput
		if !body(c, &input) {
			return
		}
		result, err := services(c).Installers.Finalize(c.Request.Context(), c.Param("id"), input)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(202, result)
	})
	routes.POST("/hosts/:id/launch-installer", func(c *gin.Context) {
		var input installer.LaunchInput
		if !body(c, &input) {
			return
		}
		result, err := services(c).Installers.Launch(c.Request.Context(), c.Param("id"), input)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(202, result)
	})
}
