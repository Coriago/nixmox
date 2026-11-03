package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func registerCacheRoutes(clustered *gin.RouterGroup) {
	clustered.GET("/cache/*path", func(c *gin.Context) {
		http.StripPrefix("/cache", services(c).Cache).ServeHTTP(c.Writer, c.Request)
	})
	clustered.HEAD("/cache/*path", func(c *gin.Context) {
		http.StripPrefix("/cache", services(c).Cache).ServeHTTP(c.Writer, c.Request)
	})
	clustered.GET("/store-cache/*path", func(c *gin.Context) {
		http.StripPrefix("/store-cache", services(c).StoreCache).ServeHTTP(c.Writer, c.Request)
	})
	clustered.HEAD("/store-cache/*path", func(c *gin.Context) {
		http.StripPrefix("/store-cache", services(c).StoreCache).ServeHTTP(c.Writer, c.Request)
	})
}
