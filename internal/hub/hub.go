// Package hub mantiene el registro de placas ESP32 conectadas por WebSocket y
// permite enviarles mensajes (a una concreta o por difusión a todas).
package hub

import (
	"log"
	"sync"
)

// Client representa una conexión WebSocket activa (una placa ESP32).
type Client struct {
	ID   string
	Addr string
	// Send es el buzón de salida del cliente. El bucle de escritura del
	// servidor lee de este canal y escribe en el socket. Usar un canal evita
	// escrituras concurrentes sobre la misma conexión (no permitidas).
	Send chan []byte
}

// Hub es el registro concurrente de clientes conectados.
type Hub struct {
	mu      sync.RWMutex
	clients map[string]*Client
}

// New crea un Hub vacío.
func New() *Hub {
	return &Hub{clients: make(map[string]*Client)}
}

// Register añade un cliente al registro.
func (h *Hub) Register(c *Client) {
	h.mu.Lock()
	h.clients[c.ID] = c
	h.mu.Unlock()
	log.Printf("[hub] cliente conectado id=%s addr=%s (total=%d)", c.ID, c.Addr, h.Count())
}

// Unregister elimina un cliente y cierra su canal de salida.
func (h *Hub) Unregister(id string) {
	h.mu.Lock()
	if c, ok := h.clients[id]; ok {
		delete(h.clients, id)
		close(c.Send)
	}
	h.mu.Unlock()
	log.Printf("[hub] cliente desconectado id=%s (total=%d)", id, h.Count())
}

// Count devuelve el número de clientes conectados.
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// ClientInfo es una vista de solo lectura de un cliente, apta para serializar.
type ClientInfo struct {
	ID   string `json:"id"`
	Addr string `json:"addr"`
}

// List devuelve la información de todos los clientes conectados.
func (h *Hub) List() []ClientInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]ClientInfo, 0, len(h.clients))
	for _, c := range h.clients {
		out = append(out, ClientInfo{ID: c.ID, Addr: c.Addr})
	}
	return out
}

// SendTo entrega payload a un cliente concreto. Devuelve false si no existe o si
// su buzón está lleno (cliente lento/atascado).
func (h *Hub) SendTo(id string, payload []byte) bool {
	h.mu.RLock()
	c, ok := h.clients[id]
	h.mu.RUnlock()
	if !ok {
		return false
	}
	select {
	case c.Send <- payload:
		return true
	default:
		log.Printf("[hub] buzón lleno, descartando mensaje para id=%s", id)
		return false
	}
}

// Broadcast entrega payload a todos los clientes y devuelve a cuántos llegó.
func (h *Hub) Broadcast(payload []byte) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for id, c := range h.clients {
		select {
		case c.Send <- payload:
			n++
		default:
			log.Printf("[hub] buzón lleno, descartando broadcast para id=%s", id)
		}
	}
	return n
}
