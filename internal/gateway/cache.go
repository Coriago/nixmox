package gateway

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"nixmox/internal/domain"
	"nixmox/internal/service"
)

func cacheRoutes(router *gin.Engine, s *service.Service) {
	for _, prefix := range []string{"/cache", "/store-cache"} {
		serve := prefix == "/store-cache"
		router.Any(prefix+"/*path", func(c *gin.Context) {
			if c.Request.Method != "GET" && c.Request.Method != "HEAD" {
				c.Header("Allow", "GET, HEAD")
				c.Status(http.StatusMethodNotAllowed)
				return
			}
			if !serve {
				host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
				if err != nil || !net.ParseIP(host).IsLoopback() {
					c.Status(http.StatusForbidden)
					return
				}
			}
			if c.Request.URL.RawQuery != "" || c.Request.URL.RawPath != "" {
				c.Status(http.StatusBadRequest)
				return
			}
			if s.Cache == nil {
				c.Status(http.StatusServiceUnavailable)
				return
			}
			ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Minute)
			defer cancel()
			c.Request = c.Request.WithContext(ctx)
			path := strings.TrimPrefix(c.Param("path"), "/")
			if path == "nix-cache-info" {
				status := s.Cache.Status(c.Request.Context())
				if (serve && !status.Serving) || (!serve && status.GatewayState == "disabled") {
					c.Status(http.StatusServiceUnavailable)
					return
				}
				cacheText(c, "text/x-nix-cache-info", "StoreDir: /nix/store\nWantMassQuery: 0\nPriority: 30\n")
				return
			}
			if strings.HasSuffix(path, ".narinfo") && !strings.Contains(path, "/") {
				info, err := s.Cache.Info(c.Request.Context(), strings.TrimSuffix(path, ".narinfo"), serve)
				if err != nil {
					cacheFailure(c, err)
					return
				}
				cacheText(c, info.ContentType(), info.String())
				return
			}
			parts := strings.Split(path, "/")
			if len(parts) != 3 || parts[0] != "nar" || !strings.HasSuffix(parts[2], ".nar") {
				c.Status(http.StatusNotFound)
				return
			}
			info, stream, err := s.Cache.Archive(c.Request.Context(), parts[1], strings.TrimSuffix(parts[2], ".nar"), serve, c.Request.Method == "HEAD")
			if err != nil {
				cacheFailure(c, err)
				return
			}
			if stream != nil {
				defer stream.Close()
			}
			c.Header("Content-Type", "application/x-nix-archive")
			c.Header("Content-Length", strconv.FormatUint(info.FileSize, 10))
			// Never cache signed metadata after signing-key loss. No buffering or Range
			// concatenation: Nix restarts interrupted downloads and verifies the NAR.
			c.Status(http.StatusOK)
			if stream == nil {
				return
			}
			writer := cacheWriter{writer: c.Writer, controller: http.NewResponseController(c.Writer)}
			if _, err = stream.WriteTo(writer); err != nil {
				panic(http.ErrAbortHandler)
			}
		})
	}
}

// Cache readiness uses the same snapshot/invalidation convention as other
// resources. Polling observes key loss even when no transfer is running.
func watchCache(c *gin.Context, s *service.Service) {
	if s.Cache == nil {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var last domain.Version
	for {
		status := s.Cache.Status(c.Request.Context())
		if status.Version != last {
			event := "snapshot"
			var value any = status
			if last != 0 {
				event = "resource.updated"
				value = map[string]any{"type": event, "resource": "cache.status", "id": status.NodeID, "version": status.Version, "observed_at": status.ObservedAt}
			}
			if sendEvent(c, event, value) != nil {
				return
			}
			last = status.Version
		}
		select {
		case <-c.Request.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func cacheText(c *gin.Context, kind, text string) {
	c.Header("Content-Type", kind)
	c.Header("Content-Length", strconv.Itoa(len(text)))
	c.Status(http.StatusOK)
	if c.Request.Method != "HEAD" {
		_, _ = io.WriteString(c.Writer, text)
	}
}
func cacheFailure(c *gin.Context, err error) {
	var detail *domain.Error
	if errors.As(err, &detail) {
		switch detail.Code {
		case "NOT_FOUND":
			c.Status(404)
			return
		case "INVALID_INPUT":
			c.Status(400)
			return
		}
	}
	c.Header("Retry-After", "1")
	c.Status(http.StatusServiceUnavailable)
}

type cacheWriter struct {
	writer     io.Writer
	controller *http.ResponseController
}

func (w cacheWriter) Write(p []byte) (int, error) {
	if err := w.controller.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return 0, err
	}
	return w.writer.Write(p)
}
