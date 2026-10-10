package swagger

import (
	"snap-flow-go/internal/auth"

	"github.com/gin-gonic/gin"
)

// RegisterRoutes protects both the UI assets and the specification.
func RegisterRoutes(router *gin.Engine, jwtManager *auth.JWTManager, specPath string) {
	docs := router.Group("")
	docs.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Next()
	}, auth.JWTMiddleware(jwtManager), auth.AdminMiddleware())
	docs.StaticFS("/swagger", Files())
	docs.GET("/openapi.yml", func(c *gin.Context) { c.File(specPath) })
}
