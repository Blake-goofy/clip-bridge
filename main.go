package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	a := newApp()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	log.Printf("clipbridge listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, a))
}
