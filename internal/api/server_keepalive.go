package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

func (s *Server) enableKeepAlive(timeout time.Duration, onTimeout func()) {
	if timeout <= 0 || onTimeout == nil {
		return
	}

	s.keepAliveEnabled = true
	s.keepAliveTimeout = timeout
	s.keepAliveOnTimeout = onTimeout
	s.keepAliveHeartbeat = make(chan struct{}, 1)
	s.keepAliveStop = make(chan struct{}, 1)

	s.engine.GET("/keep-alive", s.handleKeepAlive)

	go s.watchKeepAlive()
}

func (s *Server) handleKeepAlive(c *gin.Context) {
	if s.localPassword != "" {
		provided := strings.TrimSpace(c.GetHeader("Authorization"))
		if provided != "" {
			parts := strings.SplitN(provided, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
				provided = parts[1]
			}
		}
		if provided == "" {
			provided = strings.TrimSpace(c.GetHeader("X-Local-Password"))
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(s.localPassword)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid password"})
			return
		}
	}

	s.signalKeepAlive()
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (s *Server) signalKeepAlive() {
	if !s.keepAliveEnabled {
		return
	}
	select {
	case s.keepAliveHeartbeat <- struct{}{}:
	default:
	}
}

func (s *Server) watchKeepAlive() {
	if !s.keepAliveEnabled {
		return
	}

	timer := time.NewTimer(s.keepAliveTimeout)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			log.Warnf("keep-alive endpoint idle for %s, shutting down", s.keepAliveTimeout)
			if s.keepAliveOnTimeout != nil {
				s.keepAliveOnTimeout()
			}
			return
		case <-s.keepAliveHeartbeat:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(s.keepAliveTimeout)
		case <-s.keepAliveStop:
			return
		}
	}
}
