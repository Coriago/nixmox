// Package api exposes thin HTTP and SSE adapters over daemon-owned services.
package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"

	"nixmox/internal/cluster"
	"nixmox/internal/configuration"
	"nixmox/internal/credentials"
	"nixmox/internal/deployment"
	"nixmox/internal/discovery"
	"nixmox/internal/domain"
	"nixmox/internal/enrollment"
	"nixmox/internal/installer"
	"nixmox/internal/membership"
	"nixmox/internal/recipe"
	"nixmox/internal/source"
	"nixmox/internal/task"
)

type Services struct {
	Membership     *membership.Service
	HostDisks      func(context.Context, string) (any, error)
	Enrollment     *enrollment.Service
	Pending        bool
	Discovery      cluster.Store[discovery.Record]
	Deployments    *deployment.Service
	Metrics        cluster.Metrics
	Credentials    cluster.Store[credentials.Record]
	CachePublicKey string
	Cluster        cluster.Store[domain.Cluster]
	Members        cluster.Store[domain.Member]
	Repositories   cluster.Store[source.Repository]
	Configurations *configuration.Service
	Registry       *configuration.Registry
	Recipes        *recipe.Service
	Installers     *installer.Service
	Sources        source.Manager
	Tasks          *task.Manager
	Logs           cluster.Logs
	Cache          http.Handler
	StoreCache     http.Handler
}

type Server struct {
	InviteEnrollment  func(context.Context, enrollment.Request) (enrollment.JoiningNode, error)
	ConfirmEnrollment func(context.Context, enrollment.Confirmation) error
	Discovery         *discovery.Service
	UI                http.Handler
	Health            func(context.Context) domain.Health
	CreateCluster     func(context.Context, string) (domain.Cluster, error)
	Services          func() (*Services, error)
}

func (s Server) Handler() http.Handler {
	router := gin.New()
	router.Use(otelgin.Middleware("nixmox.api"))
	router.Use(func(c *gin.Context) {
		defer func() {
			if value := recover(); value != nil {
				if value == http.ErrAbortHandler {
					panic(value)
				}
				c.AbortWithStatusJSON(500, gin.H{"error": "internal error"})
			}
		}()
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
		c.Next()
	})
	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, s.Health(c.Request.Context()))
	})
	s.registerDiscoveryRoutes(router)
	clustered := router.Group("")
	clustered.Use(func(c *gin.Context) {
		services, err := s.Services()
		if err != nil {
			c.AbortWithStatusJSON(503, gin.H{"error": "cluster unavailable"})
			return
		}
		if services.Pending {
			if c.Request.Method != http.MethodGet {
				c.AbortWithStatusJSON(403, gin.H{"error": "pending member cannot perform cluster mutations"})
				return
			}
			switch c.FullPath() {
			case "/cluster", "/hosts/:id", "/hosts/:id/metrics/stream", "/hosts/:id/disks":
			default:
				c.AbortWithStatusJSON(503, gin.H{"error": "service unavailable on pending member"})
				return
			}
		}
		c.Set("services", services)
		c.Next()
	})
	s.registerClusterRoutes(router, clustered)
	s.registerEnrollmentRoutes(router, clustered)
	registerHostRoutes(clustered)
	registerDeploymentRoutes(clustered)
	registerInstallerRoutes(clustered)
	registerRepositoryRoutes(clustered)
	registerRecipeRoutes(clustered)
	registerConfigurationRoutes(clustered)
	registerTaskRoutes(clustered)
	registerLogRoutes(clustered)
	registerCacheRoutes(clustered)
	registerCredentialRoutes(clustered)
	if s.UI != nil {
		router.NoRoute(func(c *gin.Context) {
			path := c.Request.URL.Path
			assets := path == "/" || path == "/app.js" || path == "/app.css" || path == "/app-worker.js" || path == "/manifest.webmanifest" || path == "/wasm_exec.js" || strings.HasPrefix(path, "/web/")
			if assets {
				c.Status(http.StatusOK)
				s.UI.ServeHTTP(c.Writer, c.Request)
				return
			}
			c.Status(http.StatusNotFound)
		})
	}
	return router
}
