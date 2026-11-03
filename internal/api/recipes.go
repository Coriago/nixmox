package api

import (
	"github.com/gin-gonic/gin"

	"nixmox/internal/recipe"
)

func registerRecipeRoutes(router *gin.RouterGroup) {
	recipes := router.Group("/recipes")
	recipes.Use(func(c *gin.Context) {
		if services(c).Recipes == nil {
			c.AbortWithStatus(503)
			return
		}
		c.Next()
	})
	recipes.GET("", func(c *gin.Context) {
		limit, ok := requestLimit(c)
		if !ok {
			return
		}
		records, next, err := services(c).Recipes.List(c.Request.Context(), c.Query("cursor"), limit)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, gin.H{"items": records, "nextCursor": next})
	})
	recipes.GET("/:id/versions/:version", func(c *gin.Context) {
		version, ok := requestVersion(c)
		if !ok {
			return
		}
		record, err := services(c).Recipes.Get(c.Request.Context(), c.Param("id"), version)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, record)
	})
	recipes.POST("", func(c *gin.Context) {
		var input struct {
			Name         string            `json:"name"`
			RecipeSource *recipe.Source    `json:"recipeSource,omitempty"`
			Definition   recipe.Definition `json:"sourceDefinition"`
		}
		if !body(c, &input) {
			return
		}
		var record recipe.Version
		var err error
		if input.RecipeSource != nil {
			record, err = services(c).Recipes.Import(c.Request.Context(), input.Name, *input.RecipeSource)
		} else {
			record, err = services(c).Recipes.Create(c.Request.Context(), input.Name, input.Definition)
		}
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(201, record)
	})
	recipes.PUT("/:id", func(c *gin.Context) {
		var input struct {
			ExpectedVersion uint64 `json:"expectedVersion"`
			recipe.Edit
		}
		if !body(c, &input) {
			return
		}
		if input.ExpectedVersion == 0 {
			c.Status(400)
			return
		}
		record, err := services(c).Recipes.Edit(c.Request.Context(), c.Param("id"), input.ExpectedVersion, input.Edit)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(201, record)
	})
	recipes.POST("/:id/versions/:version/sync", func(c *gin.Context) {
		version, ok := requestVersion(c)
		if !ok {
			return
		}
		record, err := services(c).Recipes.Sync(c.Request.Context(), c.Param("id"), version)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, record)
	})
}
