// Package rpc construye las tramas JSON-RPC 2.0 que entiende el firmware del
// ESP32 (EchoKit / protocolo MCP). Todos los comandos viajan como llamadas
// "tools/call" con un nombre de herramienta y sus argumentos.
package rpc

import "sync/atomic"

// idCounter genera identificadores JSON-RPC monotónicos y únicos por proceso.
var idCounter int64

// nextID devuelve el siguiente id JSON-RPC de forma segura entre goroutines.
func nextID() int64 {
	return atomic.AddInt64(&idCounter, 1)
}

// NextID expone el generador de ids para construir tramas con id explícito
// (p.ej. al validar una trama de passthrough que no traía id).
func NextID() int64 {
	return nextID()
}

// Request es una petición JSON-RPC 2.0 genérica.
type Request struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// ToolCallParams es el cuerpo de "params" para el método "tools/call" de MCP.
type ToolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// NewToolCall crea una petición tools/call con un id autogenerado. Si arguments
// es nil se serializa como un objeto vacío ({}), tal y como espera el firmware.
func NewToolCall(name string, arguments map[string]interface{}) Request {
	if arguments == nil {
		arguments = map[string]interface{}{}
	}
	return Request{
		JSONRPC: "2.0",
		ID:      nextID(),
		Method:  "tools/call",
		Params: ToolCallParams{
			Name:      name,
			Arguments: arguments,
		},
	}
}

// Reboot construye el comando para reiniciar la placa.
func Reboot() Request {
	return NewToolCall("self.reboot", nil)
}

// TakePhoto construye el comando para pedir una foto a la cámara, opcionalmente
// acompañada de una pregunta para el asistente de IA.
func TakePhoto(question string) Request {
	args := map[string]interface{}{}
	if question != "" {
		args["question"] = question
	}
	return NewToolCall("self.camera.take_photo", args)
}

// SetVolume construye el comando para fijar el volumen del altavoz (0-100).
func SetVolume(volume int) Request {
	return NewToolCall("self.audio_speaker.set_volume", map[string]interface{}{
		"volume": volume,
	})
}
