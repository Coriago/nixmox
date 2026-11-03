package api

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"nixmox/internal/source"
)

func registerRepositoryRoutes(clustered *gin.RouterGroup) {
	clustered.GET("/repos", func(c *gin.Context) {
		list(c, services(c).Repositories)
	})
	clustered.GET("/repos/:id", func(c *gin.Context) {
		record, err := services(c).Repositories.Get(c.Request.Context(), c.Param("id"))
		respondRecord(c, record, err)
	})
	clustered.POST("/repos", func(c *gin.Context) {
		var repo source.Repository
		if !body(c, &repo) {
			return
		}
		repo.ID = uuid.NewString()
		repo, err := services(c).Sources.Refresh(c.Request.Context(), repo)
		if err != nil {
			fail(c, err)
			return
		}
		if _, err = services(c).Repositories.Create(c.Request.Context(), repo.ID, repo); err != nil {
			fail(c, err)
			return
		}
		if _, err = services(c).Registry.Register(c.Request.Context(), repo, "."); err != nil {
			fail(c, err)
			return
		}
		if err = services(c).Recipes.Discover(c.Request.Context(), repo); err != nil {
			fail(c, err)
			return
		}
		c.JSON(201, repo)
	})
	clustered.POST("/repos/:id/refresh", func(c *gin.Context) {
		svc := services(c)
		record, err := svc.Repositories.Get(c.Request.Context(), c.Param("id"))
		if err != nil {
			fail(c, err)
			return
		}
		updated, err := svc.Sources.Refresh(c.Request.Context(), record.Value)
		if err != nil {
			fail(c, err)
			return
		}
		if _, err = svc.Repositories.Update(c.Request.Context(), record.Key, updated, record.Revision); err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, updated)
	})
}
