package controller

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// Handlers for REST API calls

func invoke(c *gin.Context) {
	if err := controller.Optimize(); err != nil {
		fmt.Printf("%v: skipping cycle ... reason=%s\n", time.Now().Format("15:04:05.000"), err.Error())
		c.IndentedJSON(http.StatusNotFound, gin.H{"message": "controller: " + err.Error()})
		return
	}
	c.IndentedJSON(http.StatusOK, "OK")
}
