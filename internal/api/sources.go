package api

import (
	"bytes"
	"errors"
	"io"
	"strconv"

	"github.com/gin-gonic/gin"
	"go.yaml.in/yaml/v3"
	"nixmox/internal/recipe"
	"nixmox/internal/source"
)

func registerSourceRoutes(router *gin.RouterGroup) {
	configurations := router.Group("/configurations")
	configurations.Use(func(c *gin.Context) {
		if services(c).Configurations.KV == nil {
			c.AbortWithStatusJSON(503, gin.H{"error": "configuration registry unavailable"})
			return
		}
		c.Next()
	})
	configurations.GET("", func(c *gin.Context) { list(c, services(c).Configurations) })
	configurations.GET("/:id", func(c *gin.Context) {
		record, err := services(c).Configurations.Get(c.Request.Context(), c.Param("id"))
		respondRecord(c, record, err)
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
	recipes := router.Group("/recipes")
	recipes.Use(func(c *gin.Context) {
		if services(c).Recipes == nil {
			c.AbortWithStatusJSON(503, gin.H{"error": "recipe service unavailable"})
			return
		}
		c.Next()
	})
	recipes.GET("", func(c *gin.Context) {
		limit, err := strconv.Atoi(c.DefaultQuery("limit", "100"))
		if err != nil || limit < 1 || limit > 500 {
			c.Status(400)
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
		version, err := strconv.ParseUint(c.Param("version"), 10, 64)
		if err != nil || version == 0 {
			c.Status(400)
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
			TargetRepo   source.Pin        `json:"targetRepo"`
			RecipeSource *recipe.Source    `json:"recipeSource,omitempty"`
			Definition   recipe.Definition `json:"sourceDefinition"`
			Fields       map[string]any    `json:"fields"`
		}
		if !body(c, &input) {
			return
		}
		// Source provenance is verified by reading the actual pinned definition, not
		// accepted merely because the caller attached a repository SHA to JSON.
		if input.RecipeSource != nil {
			repo, err := services(c).Repositories.Get(c.Request.Context(), input.RecipeSource.ID)
			if err != nil {
				fail(c, err)
				return
			}
			work, err := services(c).Sources.Checkout(c.Request.Context(), repo.Value, input.RecipeSource.Pin)
			if err != nil {
				fail(c, err)
				return
			}
			defer work.Close()
			data, err := work.ReadFile(input.RecipeSource.Path)
			if err != nil {
				fail(c, err)
				return
			}
			decoder := yaml.NewDecoder(bytes.NewReader(data))
			decoder.KnownFields(true)
			input.Definition = recipe.Definition{}
			if err = decoder.Decode(&input.Definition); err != nil {
				c.JSON(400, gin.H{"error": "invalid repository recipe definition"})
				return
			}
			if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
				c.JSON(400, gin.H{"error": "recipe definition must contain one document"})
				return
			}
		}
		record := recipe.Record{TargetRepo: input.TargetRepo, RecipeSource: input.RecipeSource, SourceDefinition: input.Definition, Collected: recipe.Collected{Fields: input.Fields}}
		if err := record.Normalize(c.Request.Context()); err != nil {
			c.JSON(400, gin.H{"error": "invalid recipe definition or target pin"})
			return
		}
		created, err := services(c).Recipes.Create(c.Request.Context(), record)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(201, created)
	})
	recipes.PUT("/:id", func(c *gin.Context) {
		var input struct {
			ExpectedVersion uint64 `json:"expectedVersion"`
			recipe.Patch
		}
		if !body(c, &input) {
			return
		}
		if input.ExpectedVersion == 0 {
			current, err := services(c).Recipes.Latest(c.Request.Context(), c.Param("id"))
			if err != nil {
				fail(c, err)
				return
			}
			input.ExpectedVersion = current.Version
		}
		updated, err := services(c).Recipes.Update(c.Request.Context(), c.Param("id"), input.ExpectedVersion, input.Patch)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(201, updated)
	})
	recipes.POST("/:id/versions/:version/observe", func(c *gin.Context) {
		var input struct {
			Name string `json:"name,omitempty"`
		}
		if !body(c, &input) {
			return
		}
		version, err := strconv.ParseUint(c.Param("version"), 10, 64)
		if err != nil || version == 0 {
			c.Status(400)
			return
		}
		collector := services(c).Collector
		if collector == nil {
			c.Status(503)
			return
		}
		updated, err := services(c).Recipes.Observe(c.Request.Context(), c.Param("id"), version, input.Name, collector.Collect)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(201, updated)
	})
}
