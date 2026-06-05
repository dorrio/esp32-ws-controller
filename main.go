// Comando esp32ws: servidor de WebSockets que actúa como controlador remoto de
// una placa ESP32 (asistente de IA EchoKit). Las placas se conectan por
// WebSocket a /ws y los comandos se inyectan vía la API HTTP (POST /api/...).
//
// Uso:
//
//	go run .                 # escucha en :8080
//	go run . -addr :9000     # otro puerto
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"esp32ws/internal/server"
)

func main() {
	// Valor por defecto: PORT del entorno (lo inyectan muchas PaaS como Dokploy)
	// o :8080. El flag -addr tiene prioridad si se indica explícitamente.
	defaultAddr := ":8080"
	if p := os.Getenv("PORT"); p != "" {
		defaultAddr = ":" + p
	}
	addr := flag.String("addr", defaultAddr, "dirección de escucha (host:puerto)")
	healthcheck := flag.Bool("healthcheck", false, "consulta /healthz y termina (0=ok, 1=fallo); para Docker HEALTHCHECK")
	flag.Parse()

	// Modo sonda: usado por el HEALTHCHECK del contenedor (imagen distroless sin
	// shell ni curl). Hace una petición al propio servidor y sale con 0/1.
	if *healthcheck {
		runHealthcheck(*addr)
		return
	}

	srv := server.New()
	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Arrancamos el servidor en segundo plano.
	go func() {
		log.Printf("servidor escuchando en %s (dirección interna del contenedor)", *addr)
		if pu := os.Getenv("PUBLIC_URL"); pu != "" {
			log.Printf("URL pública: %s", pu)
		} else {
			log.Printf("URL pública: se detectará en la primera petición (vía X-Forwarded-Host)")
		}
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("error del servidor: %v", err)
		}
	}()

	// Esperamos una señal de cierre (Ctrl+C / SIGTERM) para apagar limpiamente.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Println("apagando...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("error en el apagado: %v", err)
	}
}

// runHealthcheck consulta el endpoint /healthz del servidor local y termina el
// proceso con código 0 si responde 200, o 1 en caso contrario. Lo invoca el
// HEALTHCHECK de Docker (la imagen distroless no incluye curl/wget).
func runHealthcheck(addr string) {
	host := addr
	if len(addr) > 0 && addr[0] == ':' {
		host = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + host + "/healthz")
	if err != nil {
		log.Printf("healthcheck fallido: %v", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("healthcheck: estado %d", resp.StatusCode)
		os.Exit(1)
	}
}
