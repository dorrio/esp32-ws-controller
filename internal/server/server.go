// Package server expone el WebSocket de las placas y la API HTTP de control.
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"esp32ws/internal/hub"
	"esp32ws/internal/rpc"
)

// Parámetros del ciclo de vida del WebSocket.
const (
	writeWait      = 10 * time.Second    // tiempo máximo para escribir un frame
	pongWait       = 60 * time.Second    // sin pong en este plazo => conexión muerta
	pingPeriod     = (pongWait * 9) / 10 // enviar ping algo antes del pongWait
	maxMessageSize = 1 << 20             // 1 MiB (suficiente para fotos en base64 pequeñas)
	sendBuffer     = 32                  // mensajes en cola por cliente
)

// upgrader convierte una petición HTTP en una conexión WebSocket. CheckOrigin
// rechaza cualquier petición que traiga cabecera Origin: el firmware del ESP32
// no la envía, pero un navegador sí, así que esto bloquea el secuestro
// cross-site (Cross-Site WebSocket Hijacking).
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
}

// clientSeq genera ids legibles para cada placa que se conecta.
var clientSeq int64

// Config contiene los secretos de seguridad del servidor.
type Config struct {
	// ControlToken protege la API HTTP /api/*. Si está vacío, la API queda
	// deshabilitada (responde 503).
	ControlToken string
	// DeviceKey protege el WebSocket /ws. Si está vacía, /ws acepta cualquier
	// placa (solo se recomienda en red local de confianza).
	DeviceKey string
}

// ConfigFromEnv lee la configuración de las variables de entorno CONTROL_TOKEN
// y DEVICE_KEY.
func ConfigFromEnv() Config {
	return Config{
		ControlToken: os.Getenv("CONTROL_TOKEN"),
		DeviceKey:    os.Getenv("DEVICE_KEY"),
	}
}

// Server agrupa el hub de clientes, la configuración y las rutas HTTP/WS.
type Server struct {
	hub          *hub.Hub
	controlToken string
	deviceKey    string
}

// New crea un Server tomando la configuración del entorno.
func New() *Server {
	return NewWithConfig(ConfigFromEnv())
}

// NewWithConfig crea un Server con una configuración explícita (útil en tests).
func NewWithConfig(cfg Config) *Server {
	if cfg.ControlToken == "" {
		log.Println("[seguridad] AVISO: CONTROL_TOKEN no configurado; la API /api/* está DESHABILITADA (503)")
	}
	if cfg.DeviceKey == "" {
		log.Println("[seguridad] AVISO: DEVICE_KEY no configurada; /ws acepta cualquier placa (solo apto para red local)")
	}
	return &Server{
		hub:          hub.New(),
		controlToken: cfg.ControlToken,
		deviceKey:    cfg.DeviceKey,
	}
}

// Routes devuelve el handler raíz con todas las rutas registradas.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// WebSocket: aquí se conectan las placas ESP32 (autenticación propia en /ws).
	mux.HandleFunc("/ws", s.handleWS)

	// API de control: todas las rutas exigen el token de control (Bearer).
	mux.HandleFunc("/api/command", s.requireControlToken(s.handleCommand))      // alias
	mux.HandleFunc("/api/send-command", s.requireControlToken(s.handleCommand)) // nombre del enunciado
	mux.HandleFunc("/api/clients", s.requireControlToken(s.handleClients))
	mux.HandleFunc("/api/reboot", s.requireControlToken(s.handleReboot))
	mux.HandleFunc("/api/photo", s.requireControlToken(s.handlePhoto))
	mux.HandleFunc("/api/volume", s.requireControlToken(s.handleVolume))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	return logRequests(mux)
}

// ---------------------------------------------------------------------------
// WebSocket
// ---------------------------------------------------------------------------

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	// Autenticación de la placa antes del upgrade (clave de dispositivo y
	// rechazo de orígenes de navegador).
	if reason := s.authorizeBoard(r); reason != "" {
		log.Printf("[ws] conexión rechazada desde %s: %s", r.RemoteAddr, reason)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ws] error al actualizar: %v", err)
		return
	}

	id := "esp32-" + strconv.FormatInt(atomic.AddInt64(&clientSeq, 1), 10)
	client := &hub.Client{
		ID:   id,
		Addr: r.RemoteAddr,
		Send: make(chan []byte, sendBuffer),
	}
	s.hub.Register(client)

	// Dos goroutines por conexión: una lee del socket y otra escribe.
	go s.writePump(conn, client)
	s.readPump(conn, client) // bloquea hasta que la conexión se cierra
}

// readPump lee mensajes entrantes de la placa (típicamente respuestas JSON-RPC).
func (s *Server) readPump(conn *websocket.Conn, client *hub.Client) {
	defer func() {
		s.hub.Unregister(client.ID)
		_ = conn.Close()
	}()

	conn.SetReadLimit(maxMessageSize)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("[ws] cierre inesperado id=%s: %v", client.ID, err)
			}
			break
		}
		// %q escapa caracteres de control para evitar inyección/falsificación
		// en el log a través de un frame manipulado.
		log.Printf("[ws] <- %s: %q", client.ID, message)
	}
}

// writePump envía al socket los mensajes encolados y manda pings periódicos.
func (s *Server) writePump(conn *websocket.Conn, client *hub.Client) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = conn.Close()
	}()

	for {
		select {
		case message, ok := <-client.Send:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// El hub cerró el canal: avisamos del cierre y salimos.
				_ = conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, message); err != nil {
				log.Printf("[ws] error de escritura id=%s: %v", client.ID, err)
				return
			}
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// ---------------------------------------------------------------------------
// API HTTP de control
// ---------------------------------------------------------------------------

// commandBody es el cuerpo aceptado por POST /api/command.
//
// Admite dos modos:
//  1. Paso directo de una trama JSON-RPC completa (campo "jsonrpc" presente).
//  2. Forma simplificada: {"name": "self.reboot", "arguments": {...}} que el
//     servidor envuelve en una llamada tools/call.
//
// En ambos casos "client_id" es opcional: si se omite, se difunde a todas las
// placas conectadas.
type commandBody struct {
	ClientID  string                 `json:"client_id,omitempty"`
	JSONRPC   string                 `json:"jsonrpc,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "usa POST")
		return
	}

	// Leemos el cuerpo crudo para poder reenviarlo tal cual en modo passthrough.
	raw, err := readBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "no se pudo leer el cuerpo: "+err.Error())
		return
	}

	var body commandBody
	if err := json.Unmarshal(raw, &body); err != nil {
		writeError(w, http.StatusBadRequest, "JSON inválido: "+err.Error())
		return
	}

	var payload []byte
	switch {
	case body.JSONRPC != "":
		// Modo passthrough VALIDADO: no reenviamos los bytes crudos. Parseamos
		// estrictamente, exigimos jsonrpc 2.0 + method tools/call + nombre de
		// herramienta, y re-serializamos desde campos validados (se descartan
		// claves desconocidas). Esto evita invocar métodos arbitrarios.
		req, errMsg := parsePassthrough(raw)
		if errMsg != "" {
			writeError(w, http.StatusBadRequest, errMsg)
			return
		}
		payload, _ = json.Marshal(req)
	case body.Name != "":
		// Modo simplificado: construimos la trama JSON-RPC.
		payload, _ = json.Marshal(rpc.NewToolCall(body.Name, body.Arguments))
	default:
		writeError(w, http.StatusBadRequest,
			`indica "jsonrpc" (trama completa) o "name" (forma simplificada)`)
		return
	}

	s.dispatch(w, body.ClientID, payload)
}

// parsePassthrough valida una trama JSON-RPC completa recibida por la API y
// devuelve una petición re-construida (sin campos desconocidos) o un mensaje de
// error. Solo admite el método "tools/call" con un nombre de herramienta.
func parsePassthrough(raw []byte) (rpc.Request, string) {
	var in struct {
		JSONRPC string             `json:"jsonrpc"`
		ID      *int64             `json:"id"`
		Method  string             `json:"method"`
		Params  rpc.ToolCallParams `json:"params"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return rpc.Request{}, "JSON inválido: " + err.Error()
	}
	if in.JSONRPC != "2.0" {
		return rpc.Request{}, `"jsonrpc" debe ser "2.0"`
	}
	if in.Method != "tools/call" {
		return rpc.Request{}, `solo se permite el método "tools/call"`
	}
	if in.Params.Name == "" {
		return rpc.Request{}, `falta "params.name" (nombre de la herramienta)`
	}
	if in.Params.Arguments == nil {
		in.Params.Arguments = map[string]interface{}{}
	}
	id := rpc.NextID()
	if in.ID != nil {
		id = *in.ID // respetamos el id del cliente si lo proporcionó
	}
	return rpc.Request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "tools/call",
		Params:  in.Params,
	}, ""
}

func (s *Server) handleReboot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "usa POST")
		return
	}
	payload, _ := json.Marshal(rpc.Reboot())
	s.dispatch(w, r.URL.Query().Get("client_id"), payload)
}

func (s *Server) handlePhoto(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "usa POST")
		return
	}
	question := r.URL.Query().Get("question")
	payload, _ := json.Marshal(rpc.TakePhoto(question))
	s.dispatch(w, r.URL.Query().Get("client_id"), payload)
}

func (s *Server) handleVolume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "usa POST")
		return
	}
	volume, err := strconv.Atoi(r.URL.Query().Get("volume"))
	if err != nil || volume < 0 || volume > 100 {
		writeError(w, http.StatusBadRequest, "parámetro 'volume' debe ser un entero 0-100")
		return
	}
	payload, _ := json.Marshal(rpc.SetVolume(volume))
	s.dispatch(w, r.URL.Query().Get("client_id"), payload)
}

func (s *Server) handleClients(w http.ResponseWriter, r *http.Request) {
	// No exponemos RemoteAddr (dato de red): solo el id estable de cada placa.
	infos := s.hub.List()
	ids := make([]string, 0, len(infos))
	for _, c := range infos {
		ids = append(ids, c.ID)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"count":   s.hub.Count(),
		"clients": ids,
	})
}

// dispatch envía payload a un cliente concreto o por difusión y responde al HTTP.
func (s *Server) dispatch(w http.ResponseWriter, clientID string, payload []byte) {
	if s.hub.Count() == 0 {
		writeError(w, http.StatusServiceUnavailable, "no hay placas conectadas")
		return
	}

	if clientID != "" {
		if ok := s.hub.SendTo(clientID, payload); !ok {
			writeError(w, http.StatusNotFound, "cliente no encontrado o no disponible: "+clientID)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status": "sent", "target": clientID, "payload": json.RawMessage(payload),
		})
		return
	}

	n := s.hub.Broadcast(payload)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "broadcast", "delivered": n, "payload": json.RawMessage(payload),
	})
}
