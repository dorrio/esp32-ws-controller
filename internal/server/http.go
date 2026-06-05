package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"
)

// maxBodyBytes limita el tamaño del cuerpo HTTP para evitar abusos de memoria.
const maxBodyBytes = 1 << 20 // 1 MiB

// readBody lee el cuerpo de la petición con un límite de tamaño.
func readBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
}

// writeJSON serializa v como JSON con el código de estado dado.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[http] error al escribir JSON: %v", err)
	}
}

// writeError responde con un cuerpo JSON {"error": msg}.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// logRequests es un middleware que registra cada petición HTTP entrante.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("[http] %s %s (%s)", r.Method, r.URL.Path, time.Since(start))
	})
}
