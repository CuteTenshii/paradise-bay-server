package main

import "log"

func main() {
	store, err := OpenDB("paradise-bay.db")
	if err != nil {
		log.Fatal("failed to open database:", err)
	}
	defer store.db.Close()

	go StartHttp(3300)
	StartSocket(3301, store)
}
