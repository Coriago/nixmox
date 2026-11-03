package api

import (
	"github.com/gin-gonic/gin"
)

func (s Server) registerClusterRoutes(router *gin.Engine, clustered *gin.RouterGroup) {
	router.POST("/cluster", func(c *gin.Context) {
		var input struct {
			Name string `json:"name"`
		}
		if !body(c, &input) {
			return
		}
		result, err := s.CreateCluster(c.Request.Context(), input.Name)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(201, result)
	})
	clustered.GET("/cluster", func(c *gin.Context) {
		record, err := services(c).Cluster.Get(c.Request.Context(), "identity")
		respondRecord(c, record, err)
	})
}
