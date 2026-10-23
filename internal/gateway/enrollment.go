package gateway

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"nixmox/internal/domain"
	"nixmox/internal/service"
)

func enrollment(api *gin.RouterGroup, s *service.Service) {
	api.GET("/membership-topology", func(c *gin.Context) {
		if s.Cluster == nil {
			failure(c, domain.Unavailable("membership"), nil)
			return
		}
		topology, err := s.Cluster.Topology()
		if err != nil {
			failure(c, err, nil)
			return
		}
		c.JSON(http.StatusOK, topology)
	})
	api.GET("/membership-status", func(c *gin.Context) {
		epoch, err := strconv.ParseUint(c.Query("membership_epoch"), 10, 64)
		if err != nil || epoch == 0 || c.Query("node_id") == "" || !queries(c, "node_id", "membership_epoch") {
			failure(c, &domain.Error{Code: "INVALID_INPUT", Message: "Node ID and membership epoch are required"}, nil)
			return
		}
		if s.Cluster == nil {
			failure(c, domain.Unavailable("membership"), nil)
			return
		}
		notice, err := s.Cluster.Removal(domain.ID(c.Query("node_id")), epoch)
		if err != nil {
			failure(c, err, nil)
			return
		}
		c.JSON(http.StatusOK, notice)
	})
	group := api.Group("/enrollment/requests")
	group.Use(func(c *gin.Context) {
		if s.Cluster == nil {
			failure(c, domain.Unavailable("enrollment"), nil)
			return
		}
		c.Next()
	})
	group.POST("", func(c *gin.Context) {
		media, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if err != nil || media != "application/json" {
			failure(c, &domain.Error{Code: "INVALID_INPUT", Message: "Expected application/json"}, nil)
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 24<<10)
		decoder := json.NewDecoder(c.Request.Body)
		decoder.DisallowUnknownFields()
		var request domain.EnrollmentSubmission
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
			failure(c, &domain.Error{Code: "INVALID_INPUT", Message: "Invalid enrollment JSON"}, nil)
			return
		}
		result, err := s.Cluster.Enroll(c.Request.Context(), request)
		if err != nil {
			failure(c, err, nil)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	group.GET("/:id", func(c *gin.Context) {
		result, err := s.Cluster.Enrollment(c.Request.Context(), domain.ID(c.Param("id")))
		if err != nil {
			failure(c, err, nil)
			return
		}
		c.JSON(http.StatusOK, result)
	})
	group.POST("/:id/certificates", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
		if _, err := io.Copy(io.Discard, c.Request.Body); err != nil {
			failure(c, &domain.Error{Code: "INVALID_INPUT", Message: "Certificate request too large"}, nil)
			return
		}
		result, err := s.Cluster.Certificates(c.Request.Context(), domain.ID(c.Param("id")))
		if err != nil {
			failure(c, err, nil)
			return
		}
		c.JSON(http.StatusOK, result)
	})
}
