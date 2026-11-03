package api

import (
	"github.com/gin-gonic/gin"

	"nixmox/internal/configuration"
	"nixmox/internal/recipe"
	"nixmox/internal/source"
)

func registerConfigurationRoutes(router *gin.RouterGroup) {
	configurations := router.Group("/configurations")
	configurations.Use(func(c *gin.Context) {
		if services(c).Configurations == nil {
			c.AbortWithStatus(503)
			return
		}
		c.Next()
	})
	configurations.GET("", func(c *gin.Context) {
		limit, ok := requestLimit(c)
		if !ok {
			return
		}
		records, next, err := services(c).Configurations.List(c.Request.Context(), c.Query("cursor"), limit)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, gin.H{"items": records, "nextCursor": next})
	})
	configurations.GET("/:id", func(c *gin.Context) {
		record, err := services(c).Configurations.Latest(c.Request.Context(), c.Param("id"))
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, record)
	})
	configurations.GET("/:id/versions/:version", func(c *gin.Context) {
		version, ok := requestVersion(c)
		if !ok {
			return
		}
		record, err := services(c).Configurations.Get(c.Request.Context(), c.Param("id"), version)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, record)
	})
	configurations.POST("", func(c *gin.Context) {
		var input configuration.Create
		if !body(c, &input) {
			return
		}
		record, err := services(c).Configurations.Create(c.Request.Context(), input)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(201, record)
	})
	configurations.PUT("/:id", func(c *gin.Context) {
		var input struct {
			ExpectedVersion uint64         `json:"expectedVersion"`
			Fields          map[string]any `json:"fields"`
		}
		if !body(c, &input) {
			return
		}
		if input.ExpectedVersion == 0 {
			c.Status(400)
			return
		}
		record, err := services(c).Configurations.SetInputs(c.Request.Context(), c.Param("id"), input.ExpectedVersion, input.Fields)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(201, record)
	})
	configurations.POST("/:id/versions/:version/upgrade", func(c *gin.Context) {
		version, ok := requestVersion(c)
		if !ok {
			return
		}
		var reference recipe.Reference
		if !body(c, &reference) {
			return
		}
		record, err := services(c).Configurations.UpgradeRecipe(c.Request.Context(), c.Param("id"), version, reference)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(201, record)
	})
	configurations.POST("/:id/versions/:version/target", func(c *gin.Context) {
		version, ok := requestVersion(c)
		if !ok {
			return
		}
		var pin source.Pin
		if !body(c, &pin) {
			return
		}
		record, err := services(c).Configurations.RefreshTarget(c.Request.Context(), c.Param("id"), version, pin)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(201, record)
	})
	configurations.POST("/:id/refresh", func(c *gin.Context) {
		registry := services(c).Registry
		if registry == nil {
			c.Status(503)
			return
		}
		record, err := registry.Refresh(c.Request.Context(), c.Param("id"))
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, record)
	})
	configurations.POST("/:id/versions/:version/observe", func(c *gin.Context) {
		version, ok := requestVersion(c)
		if !ok {
			return
		}
		var input struct {
			Name string `json:"name,omitempty"`
		}
		if !body(c, &input) {
			return
		}
		if services(c).Configurations.Observer == nil {
			c.Status(503)
			return
		}
		record, err := services(c).Configurations.Observe(c.Request.Context(), c.Param("id"), version, input.Name)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(201, record)
	})
	configurations.POST("/:id/versions/:version/render", func(c *gin.Context) {
		version, ok := requestVersion(c)
		if !ok {
			return
		}
		rendered, err := services(c).Configurations.Render(c.Request.Context(), c.Param("id"), version)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, rendered)
	})
}
