package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

func StartHttp(port int) {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/game_servers/available", func(w http.ResponseWriter, r *http.Request) {
		servers := []map[string]interface{}{
			{
				"game_server": map[string]interface{}{
					// You'll probably want to change the IP to localhost. In my case, I'm using a Windows VM to test
					// this, so I need to bind the IP for the VMware network to use my host
					"uri": "zp://127.0.0.1:3301/",
				},
			},
		}

		data, _ := json.Marshal(servers)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(data); err != nil {
			log.Fatal(err)
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
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusOK)

		if err := json.NewEncoder(w).Encode(response); err != nil {
			log.Println("failed to write response:", err)
		}
	})

	fmt.Printf("HTTP Server started on 127.0.0.1:%d\n", port)
	http.ListenAndServe(fmt.Sprintf(":%d", port), mux)
}
