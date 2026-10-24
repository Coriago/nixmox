package gateway

import (
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"
	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/service"
)

func credentialRoutes(api *gin.RouterGroup, s *service.Service) {
	api.GET("/credentials", read(s, "credentials", ""))
	api.GET("/credentials/:id", read(s, "credentials", ""))
	api.POST("/credentials/import", func(c *gin.Context) {
		media, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if err != nil || media != "application/octet-stream" {
			failure(c, &domain.Error{Code: "INVALID_INPUT", Message: "Credential import requires an application/octet-stream body"}, nil)
			return
		}
		in := engine.SecretIngress{CommandID: domain.ID(c.GetHeader("Nx-Command")), HolderID: domain.ID(c.GetHeader("Nx-Holder")), Name: c.GetHeader("Nx-Name"), Kind: c.GetHeader("Nx-Kind")}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, domain.CommandPayloadLimit)
		receipt, err := s.Import(c.Request.Context(), in, c.Request.Body)
		if err != nil {
			failure(c, err, &in.CommandID)
			return
		}
		c.JSON(http.StatusAccepted, receipt)
	})
}
