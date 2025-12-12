package main

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

func main() {
	r := gin.Default()

	r.GET("/api/game_servers/available", func(c *gin.Context) {
		servers := []gin.H{
			{
				"game_server": gin.H{
					// You'll probably want to change the IP to localhost. In my case, I'm using a Windows VM to test
					// this, so I need to bind the IP for the VMware network to use my host
					"uri": "zp://172.16.148.1:3301/",
				},
			},
		}

		data, _ := json.Marshal(servers)
		c.Data(http.StatusOK, "application/json", data)
	})

	r.POST("/clientInstrumentation/report", func(c *gin.Context) {
		var body string
		c.BindPlain(&body)
		log.Println(body)
		c.JSON(200, gin.H{
			"group":   "aaa",
			"success": true,
		})
	})

	go r.Run("0.0.0.0:3300")

	startSocket(3301)
}
