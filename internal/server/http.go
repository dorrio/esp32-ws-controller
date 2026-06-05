package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
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

// statusRecorder envuelve un ResponseWriter para capturar el código de estado.
// Implementa http.Hijacker delegando, imprescindible para que el upgrade del
// WebSocket (que necesita Hijack) siga funcionando al pasar por el middleware.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("el ResponseWriter no soporta Hijack")
	}
	return h.Hijack()
}

// logRequests es un middleware que registra método, ruta, código de estado y
// duración de cada petición. Además detecta y registra la URL pública (una sola
// vez) a partir de las cabeceras que añade el proxy inverso.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.logPublicURLOnce(r)
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("[http] %s %s %d (%s)", r.Method, r.URL.Path, rec.status, time.Since(start))
	})
}

// logPublicURLOnce registra la URL pública del servicio la primera vez que llega
// una petición. Prioridad: variable PUBLIC_URL > cabeceras X-Forwarded-* > Host.
func (s *Server) logPublicURLOnce(r *http.Request) {
	s.publicOnce.Do(func() {
		url := s.publicURL
		if url == "" {
			host := r.Header.Get("X-Forwarded-Host")
			if host == "" {
				host = r.Host
			}
			proto := r.Header.Get("X-Forwarded-Proto")
			if proto == "" {
				proto = "http"
			}
			if host != "" {
				url = proto + "://" + host
			}
		}
		if url != "" {
			log.Printf("[http] URL pública detectada: %s", url)
		}
	})
}
