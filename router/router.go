package router

import (
	"github.com/apex/log"
	"github.com/gin-gonic/gin"
)

// Configure configures the routing infrastructure for this daemon instance.
func Configure() *gin.Engine {
	gin.SetMode("release")

	router := gin.New()
	router.Use(gin.Recovery())
	//router.Use(middleware.AttachRequestID(), middleware.CaptureErrors(), middleware.SetAccessControlHeaders())
	//router.Use(middleware.AttachServerManager(m), middleware.AttachApiClient(client))
	// @todo log this into a different file so you can setup IP blocking for abusive requests and such.
	// This should still dump requests in debug mode since it does help with understanding the request
	// lifecycle and quickly seeing what was called leading to the logs. However, it isn't feasible to mix
	// this output in production and still get meaningful logs from it since they'll likely just be a huge
	// spamfest.
	router.Use(gin.LoggerWithFormatter(func(params gin.LogFormatterParams) string {
		log.WithFields(log.Fields{
			"client_ip":  params.ClientIP,
			"status":     params.StatusCode,
			"latency":    params.Latency,
			"request_id": params.Keys["request_id"],
		}).Debugf("%s %s", params.MethodColor()+params.Method+params.ResetColor(), params.Path)

		return ""
	}))

	// These routes use signed URLs to validate access to the resource being requested.
	//router.GET("/download/backup", getDownloadBackup)
	//router.GET("/download/file", getDownloadFile)
	//router.POST("/upload/file", postServerUploadFiles)

	return router
}
