// Package api exposes thin HTTP and SSE adapters over daemon-owned services.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"nixmox/internal/cluster"
	"nixmox/internal/domain"
	"nixmox/internal/host"
	"nixmox/internal/recipe"
	"nixmox/internal/source"
	"nixmox/internal/task"
)

type Services struct {
	Cluster        cluster.Store[domain.Cluster]
	Members        cluster.Store[domain.Member]
	Repositories   cluster.Store[source.Repository]
	Configurations cluster.Store[source.Configuration]
	Registry       *source.Registry
	Recipes        *recipe.Versions
	Collector      *recipe.Collector
	Sources        source.Manager
	Tasks          *task.Manager
	Logs           cluster.Logs
	Cache          http.Handler
	StoreCache     http.Handler
}
type Server struct {
	Health        func(context.Context) domain.Health
	CreateCluster func(context.Context, string) (domain.Cluster, error)
	Services      func() (*Services, error)
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
	router.GET("/health", func(c *gin.Context) { c.JSON(200, s.Health(c.Request.Context())) })
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
	clustered := router.Group("")
	clustered.Use(func(c *gin.Context) {
		services, err := s.Services()
		if err != nil {
			c.AbortWithStatusJSON(503, gin.H{"error": "cluster unavailable"})
			return
		}
		c.Set("services", services)
		c.Next()
	})
	clustered.GET("/cluster", func(c *gin.Context) {
		record, err := services(c).Cluster.Get(c.Request.Context(), "identity")
		respondRecord(c, record, err)
	})
	clustered.GET("/hosts", func(c *gin.Context) { list(c, services(c).Members) })
	clustered.GET("/hosts/:id", func(c *gin.Context) {
		record, err := services(c).Members.Get(c.Request.Context(), c.Param("id"))
		respondRecord(c, record, err)
	})
	clustered.GET("/repos", func(c *gin.Context) { list(c, services(c).Repositories) })
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
	clustered.GET("/tasks", func(c *gin.Context) { list(c, services(c).Tasks.Tasks) })
	clustered.GET("/tasks/:id", func(c *gin.Context) {
		record, err := services(c).Tasks.Tasks.Get(c.Request.Context(), c.Param("id"))
		respondRecord(c, record, err)
	})
	clustered.POST("/hosts/:id/builds", func(c *gin.Context) {
		manager := services(c).Tasks
		if c.Param("id") != manager.HostID {
			c.JSON(503, gin.H{"error": "remote host dispatch is not available"})
			return
		}
		var input host.BuildInput
		if !body(c, &input) {
			return
		}
		if err := input.TargetRepo.Validate(); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		data, err := json.Marshal(input)
		if err != nil {
			fail(c, err)
			return
		}
		result, err := manager.Submit(c.Request.Context(), domain.Task{HostID: manager.HostID, Type: "build", Inputs: data})
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(202, result)
	})
	clustered.POST("/tasks/:id/cancel", func(c *gin.Context) {
		if err := services(c).Tasks.Cancel(c.Request.Context(), c.Param("id")); err != nil {
			fail(c, err)
			return
		}
		c.Status(202)
	})
	clustered.POST("/tasks/:id/retry", func(c *gin.Context) {
		result, err := services(c).Tasks.Retry(c.Request.Context(), c.Param("id"))
		if err != nil {
			fail(c, err)
			return
		}
		c.JSON(202, result)
	})
	clustered.POST("/tasks/:id/approve", func(c *gin.Context) {
		var input struct {
			OutputID string `json:"outputId"`
		}
		if !body(c, &input) {
			return
		}
		if err := services(c).Tasks.Approve(c.Request.Context(), c.Param("id"), input.OutputID); err != nil {
			fail(c, err)
			return
		}
		c.Status(202)
	})
	clustered.GET("/tasks/:id/outputs/:output", func(c *gin.Context) {
		record, err := services(c).Tasks.Outputs.Get(c.Request.Context(), c.Param("output"))
		if err == nil && record.Value.TaskID != c.Param("id") {
			c.Status(404)
			return
		}
		respondRecord(c, record, err)
	})
	clustered.GET("/tasks/:id/events", func(c *gin.Context) {
		id := c.Param("id")
		store := services(c).Tasks.Tasks
		if _, err := store.Get(c.Request.Context(), id); err != nil {
			fail(c, err)
			return
		}
		sse(c, func(send func(string, uint64, any) error) error {
			return store.Watch(c.Request.Context(), id, func(record *cluster.Record[domain.Task], deleted bool) error {
				if record == nil || deleted {
					return nil
				}
				return send("task", record.Revision, record.Value)
			})
		})
	})
	for _, kind := range []string{"host", "task"} {
		prefix := "/" + kind + "s/:id/logs"
		clustered.GET(prefix, func(c *gin.Context) {
			after, err := strconv.ParseUint(c.DefaultQuery("after", "0"), 10, 64)
			if err != nil {
				c.Status(400)
				return
			}
			result, err := services(c).Logs.Read(c.Request.Context(), kind, c.Param("id"), after, 100)
			if err != nil {
				fail(c, err)
				return
			}
			c.JSON(200, result)
		})
		clustered.GET(prefix+"/stream", func(c *gin.Context) {
			cursor := c.GetHeader("Last-Event-ID")
			if cursor == "" {
				cursor = c.DefaultQuery("after", "0")
			}
			after, err := strconv.ParseUint(cursor, 10, 64)
			if err != nil {
				c.Status(400)
				return
			}
			sse(c, func(send func(string, uint64, any) error) error {
				return services(c).Logs.Follow(c.Request.Context(), kind, c.Param("id"), after, func(entry cluster.LogEntry) error { return send("log", entry.Sequence, entry.Data) })
			})
		})
	}
	clustered.GET("/cache/*path", func(c *gin.Context) { http.StripPrefix("/cache", services(c).Cache).ServeHTTP(c.Writer, c.Request) })
	clustered.HEAD("/cache/*path", func(c *gin.Context) { http.StripPrefix("/cache", services(c).Cache).ServeHTTP(c.Writer, c.Request) })
	clustered.GET("/store-cache/*path", func(c *gin.Context) {
		http.StripPrefix("/store-cache", services(c).StoreCache).ServeHTTP(c.Writer, c.Request)
	})
	clustered.HEAD("/store-cache/*path", func(c *gin.Context) {
		http.StripPrefix("/store-cache", services(c).StoreCache).ServeHTTP(c.Writer, c.Request)
	})
	registerSourceRoutes(clustered)
	return router
}

func services(c *gin.Context) *Services { return c.MustGet("services").(*Services) }
func body(c *gin.Context, destination any) bool {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		c.AbortWithStatusJSON(400, gin.H{"error": "invalid JSON request"})
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		c.AbortWithStatusJSON(400, gin.H{"error": "request must contain one JSON value"})
		return false
	}
	return true
}
func fail(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := "operation failed"
	if errors.Is(err, jetstream.ErrKeyNotFound) || errors.Is(err, jetstream.ErrKeyDeleted) {
		status = 404
		message = "record not found"
	}
	if errors.Is(err, jetstream.ErrKeyExists) {
		status = 409
		message = "record changed; refresh and retry"
	}
	c.JSON(status, gin.H{"error": message})
}
func respondRecord[T any](c *gin.Context, record cluster.Record[T], err error) {
	if err != nil {
		fail(c, err)
		return
	}
	c.Header("ETag", `"`+strconv.FormatUint(record.Revision, 10)+`"`)
	c.JSON(200, record.Value)
}
func list[T any](c *gin.Context, store cluster.Store[T]) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if err != nil || limit < 1 || limit > 500 {
		c.Status(400)
		return
	}
	records, next, err := store.List(c.Request.Context(), c.Query("cursor"), limit)
	if err != nil {
		fail(c, err)
		return
	}
	items := make([]T, 0, len(records))
	for _, record := range records {
		items = append(items, record.Value)
	}
	c.JSON(200, gin.H{"items": items, "nextCursor": next})
}
func sse(c *gin.Context, stream func(func(string, uint64, any) error) error) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	controller := http.NewResponseController(c.Writer)
	err := stream(func(event string, id uint64, value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_ = controller.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if _, err = io.WriteString(c.Writer, "id: "+strconv.FormatUint(id, 10)+"\nevent: "+event+"\ndata: "+string(data)+"\n\n"); err != nil {
			return err
		}
		return controller.Flush()
	})
	if err != nil && c.Request.Context().Err() == nil && !c.Writer.Written() {
		c.Header("Content-Type", "application/json")
		fail(c, err)
	}
}
