package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
)

func main() {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/game_servers/available", func(w http.ResponseWriter, r *http.Request) {
		servers := []map[string]interface{}{
			{
				"game_server": map[string]interface{}{
					// You'll probably want to change the IP to localhost. In my case, I'm using a Windows VM to test
					// this, so I need to bind the IP for the VMware network to use my host
					"uri": "zp://172.16.148.1:3301/",
				},
			},
		}

		data, _ := json.Marshal(servers)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		if err := json.NewEncoder(w).Encode(data); err != nil {
			log.Println("failed to write response:", err)
		}
	})

	mux.HandleFunc("/clientInstrumentation/report", func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		body := string(bodyBytes)
		log.Println(body)

		// Write response
		response := map[string]interface{}{"success": true}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		if err := json.NewEncoder(w).Encode(response); err != nil {
			log.Println("failed to write response:", err)
		}
	})

	go http.ListenAndServe(":3300", mux)
	startSocket(3301)
}
