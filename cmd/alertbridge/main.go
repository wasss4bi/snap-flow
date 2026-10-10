package main

import (
	"log"
	"net/http"
	"os"
	"snap-flow-go/internal/alertbridge"
	"time"
)

func main() {
	handler := alertbridge.Handler{Token: os.Getenv("TELEGRAM_BOT_TOKEN"), ChatID: os.Getenv("TELEGRAM_CHAT_ID")}
	if handler.Token == "" || handler.ChatID == "" {
		log.Print("Telegram notifications disabled: TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID are required")
	}
	mux := http.NewServeMux()
	mux.Handle("/alerts", handler)
	server := &http.Server{Addr: ":9094", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(server.ListenAndServe())
}
