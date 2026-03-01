package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
)

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.statusCode = code
	lrw.ResponseWriter.WriteHeader(code)
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(lrw, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.String(), lrw.statusCode, time.Since(start).Truncate(time.Millisecond))
	})
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required environment variable %s is not set", key)
	}
	return v
}

func main() {
	_ = godotenv.Load()

	adminPassword := mustEnv("ADMIN_PASSWORD")
	serverPort := getEnv("SERVER_PORT", "8080")
	dataDir := getEnv("DATA_DIR", "./data")

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("failed to create data directory %s: %v", dataDir, err)
	}

	app, err := NewApp(dataDir, adminPassword)
	if err != nil {
		log.Fatalf("failed to initialize app: %v", err)
	}
	defer app.Close()

	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	mux.HandleFunc("/", app.SubmitFormHandler)
	mux.HandleFunc("/submit", app.SubmitTicketHandler)
	mux.HandleFunc("/tickets", app.TicketsLookupHandler)
	mux.HandleFunc("/ticket/", app.TicketDetailHandler)
	mux.HandleFunc("/tips", app.TipsPageHandler)
	mux.HandleFunc("/admin", app.AdminDashboardHandler)
	mux.HandleFunc("/admin/login", app.AdminLoginHandler)
	mux.HandleFunc("/admin/ticket/", app.AdminUpdateTicketHandler)
	mux.HandleFunc("/admin/tips", app.AdminTipsHandler)
	mux.HandleFunc("/healthz", app.HealthHandler)

	addr := ":" + serverPort
	log.Printf("server listening on http://localhost%s", addr)
	log.Fatal(http.ListenAndServe(addr, loggingMiddleware(mux)))
}
