package api

import "github.com/gin-gonic/gin"

func (s Server) registerDiscoveryRoutes(router *gin.Engine) {
	routes := router.Group("/discovery")
	routes.Use(func(c *gin.Context) {
		if s.Discovery == nil {
			c.AbortWithStatus(503)
			return
		}
		c.Next()
	})
	routes.GET("", func(c *gin.Context) {
		limit, ok := requestLimit(c)
		if !ok {
			return
		}
		records, next, err := s.Discovery.List(c.Request.Context(), c.Query("cursor"), limit)
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, gin.H{"items": records, "nextCursor": next})
	})
	routes.GET("/:id", func(c *gin.Context) {
		record, err := s.Discovery.Get(c.Request.Context(), c.Param("id"))
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, record)
	})
}
