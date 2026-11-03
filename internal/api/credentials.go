package api

import (
	"github.com/gin-gonic/gin"

	"nixmox/internal/credentials"
)

// Only public metadata crosses this API, never stored SOPS content.
type credentialMetadata struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func publicCredential(record credentials.Record) credentialMetadata {
	return credentialMetadata{ID: record.ID, Name: record.Name, Type: record.Type}
}

func registerCredentialRoutes(routes *gin.RouterGroup) {
	routes.GET("/credentials", func(c *gin.Context) {
		store := services(c).Credentials
		if store.KV == nil {
			c.Status(503)
			return
		}
		limit, ok := requestLimit(c)
		if !ok {
			return
		}
		records, next, err := store.List(c.Request.Context(), c.Query("cursor"), limit)
		if err != nil {
			fail(c, err)
			return
		}
		items := make([]credentialMetadata, 0, len(records))
		for _, record := range records {
			items = append(items, publicCredential(record.Value))
		}
		c.JSON(200, gin.H{"items": items, "nextCursor": next})
	})
	routes.GET("/credentials/:id", func(c *gin.Context) {
		store := services(c).Credentials
		if store.KV == nil {
			c.Status(503)
			return
		}
		record, err := store.Get(c.Request.Context(), c.Param("id"))
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(200, publicCredential(record.Value))
	})
}
