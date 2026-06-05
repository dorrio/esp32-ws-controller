// Comando fake-esp32: cliente de prueba que simula una placa ESP32. Se conecta
// al servidor por WebSocket y muestra por consola cada comando JSON-RPC que
// recibe. Útil para probar la API de control sin hardware real.
//
// Uso:
//
//	go run ./cmd/fake-esp32                       # conecta a ws://localhost:8080/ws
//	go run ./cmd/fake-esp32 -url ws://host:9000/ws
package main

import (
	"flag"
	"log"
	url2 "net/url"
	"strings"

	"github.com/gorilla/websocket"
)

func main() {
	url := flag.String("url", "ws://localhost:8080/ws", "URL del WebSocket del servidor")
	key := flag.String("key", "", "clave de dispositivo (se añade como ?key=... si el servidor la exige)")
	flag.Parse()

	target := *url
	if *key != "" {
		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		target += sep + "key=" + url2.QueryEscape(*key)
	}

	conn, _, err := websocket.DefaultDialer.Dial(target, nil)
	if err != nil {
		log.Fatalf("no se pudo conectar a %s: %v", *url, err)
	}
	defer conn.Close()
	log.Printf("ESP32 simulado conectado a %s. Esperando comandos...", *url)

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("conexión cerrada: %v", err)
			return
		}
		log.Printf("comando recibido: %q", message)
	}
}
