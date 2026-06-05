package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// testToken es el token de control usado en las pruebas.
const testToken = "test-secret-token"

// newTestServer crea un servidor de pruebas con token de control y, opcionalmente,
// clave de dispositivo.
func newTestServer(deviceKey string) http.Handler {
	return NewWithConfig(Config{ControlToken: testToken, DeviceKey: deviceKey}).Routes()
}

// postAuth hace un POST autenticado con el token de control.
func postAuth(t *testing.T, url, body string) *http.Response {
	t.Helper()
	var r *strings.Reader
	if body == "" {
		r = strings.NewReader("")
	} else {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(http.MethodPost, url, r)
	if err != nil {
		t.Fatalf("crear request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST falló: %v", err)
	}
	return resp
}

// connectFakeESP32 abre una conexión WebSocket contra el servidor de pruebas,
// simulando una placa ESP32. query se añade a la URL (p.ej. "?key=...").
func connectFakeESP32(t *testing.T, httpURL, query string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(httpURL, "http") + "/ws" + query
	return websocket.DefaultDialer.Dial(wsURL, nil)
}

// mustConnect conecta y falla el test si no lo consigue.
func mustConnect(t *testing.T, httpURL, query string) *websocket.Conn {
	t.Helper()
	conn, _, err := connectFakeESP32(t, httpURL, query)
	if err != nil {
		t.Fatalf("no se pudo conectar el ESP32 simulado: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // dar tiempo al registro en el hub
	return conn
}

// readJSONRPC lee el siguiente mensaje del socket como petición JSON-RPC.
func readJSONRPC(t *testing.T, conn *websocket.Conn) map[string]interface{} {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("no se recibió mensaje: %v", err)
	}
	var msg map[string]interface{}
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("mensaje no es JSON válido: %v", err)
	}
	return msg
}

func TestBroadcastSimplifiedCommand(t *testing.T) {
	ts := httptest.NewServer(newTestServer(""))
	defer ts.Close()

	conn := mustConnect(t, ts.URL, "")
	defer conn.Close()

	resp := postAuth(t, ts.URL+"/api/command",
		`{"name":"self.audio_speaker.set_volume","arguments":{"volume":80}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("esperaba 200, obtuve %d", resp.StatusCode)
	}

	msg := readJSONRPC(t, conn)
	if msg["jsonrpc"] != "2.0" || msg["method"] != "tools/call" {
		t.Errorf("trama inesperada: %v", msg)
	}
	params := msg["params"].(map[string]interface{})
	if params["name"] != "self.audio_speaker.set_volume" {
		t.Errorf("name = %v", params["name"])
	}
	args := params["arguments"].(map[string]interface{})
	if args["volume"].(float64) != 80 {
		t.Errorf("volume = %v, quería 80", args["volume"])
	}
}

func TestPassthroughJSONRPC(t *testing.T) {
	ts := httptest.NewServer(newTestServer(""))
	defer ts.Close()

	conn := mustConnect(t, ts.URL, "")
	defer conn.Close()

	raw := `{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{"name":"self.reboot","arguments":{}}}`
	resp := postAuth(t, ts.URL+"/api/send-command", raw)
	defer resp.Body.Close()

	msg := readJSONRPC(t, conn)
	if msg["id"].(float64) != 42 {
		t.Errorf("id = %v, quería 42 (debe respetar el de la trama)", msg["id"])
	}
	params := msg["params"].(map[string]interface{})
	if params["name"] != "self.reboot" {
		t.Errorf("name = %v, quería self.reboot", params["name"])
	}
}

func TestPassthroughRejectsForeignMethod(t *testing.T) {
	ts := httptest.NewServer(newTestServer(""))
	defer ts.Close()

	conn := mustConnect(t, ts.URL, "")
	defer conn.Close()

	// Un método distinto de tools/call debe rechazarse con 400.
	raw := `{"jsonrpc":"2.0","id":1,"method":"system.exec","params":{"name":"x"}}`
	resp := postAuth(t, ts.URL+"/api/command", raw)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("esperaba 400 para método no permitido, obtuve %d", resp.StatusCode)
	}
}

func TestConvenienceEndpoints(t *testing.T) {
	ts := httptest.NewServer(newTestServer(""))
	defer ts.Close()

	conn := mustConnect(t, ts.URL, "")
	defer conn.Close()

	resp := postAuth(t, ts.URL+"/api/photo?question=¿Qué%20ves?", "")
	defer resp.Body.Close()

	msg := readJSONRPC(t, conn)
	params := msg["params"].(map[string]interface{})
	if params["name"] != "self.camera.take_photo" {
		t.Errorf("name = %v, quería self.camera.take_photo", params["name"])
	}
	args := params["arguments"].(map[string]interface{})
	if args["question"] != "¿Qué ves?" {
		t.Errorf("question = %v", args["question"])
	}
}

func TestNoClientsReturns503(t *testing.T) {
	ts := httptest.NewServer(newTestServer(""))
	defer ts.Close()

	resp := postAuth(t, ts.URL+"/api/reboot", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("esperaba 503 sin placas conectadas, obtuve %d", resp.StatusCode)
	}
}

func TestClientsEndpoint(t *testing.T) {
	ts := httptest.NewServer(newTestServer(""))
	defer ts.Close()

	conn := mustConnect(t, ts.URL, "")
	defer conn.Close()

	resp := postAuth(t, ts.URL+"/api/clients", "")
	defer resp.Body.Close()

	var out struct {
		Count   int      `json:"count"`
		Clients []string `json:"clients"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode falló: %v", err)
	}
	if out.Count != 1 {
		t.Errorf("count = %d, quería 1", out.Count)
	}
}

func TestInvalidBodyReturns400(t *testing.T) {
	ts := httptest.NewServer(newTestServer(""))
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/command",
		bytes.NewReader([]byte("esto no es json")))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST falló: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("esperaba 400, obtuve %d", resp.StatusCode)
	}
}

// --- Seguridad -------------------------------------------------------------

func TestAPIRequiresToken(t *testing.T) {
	ts := httptest.NewServer(newTestServer(""))
	defer ts.Close()

	// Sin cabecera Authorization.
	resp, err := http.Post(ts.URL+"/api/reboot", "application/json", nil)
	if err != nil {
		t.Fatalf("POST falló: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("esperaba 401 sin token, obtuve %d", resp.StatusCode)
	}
}

func TestAPIRejectsWrongToken(t *testing.T) {
	ts := httptest.NewServer(newTestServer(""))
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/reboot", nil)
	req.Header.Set("Authorization", "Bearer token-equivocado")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST falló: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("esperaba 401 con token erróneo, obtuve %d", resp.StatusCode)
	}
}

func TestAPIDisabledWithoutToken(t *testing.T) {
	// Servidor sin CONTROL_TOKEN: la API debe estar deshabilitada (503).
	ts := httptest.NewServer(NewWithConfig(Config{}).Routes())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/reboot", nil)
	req.Header.Set("Authorization", "Bearer cualquiera")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST falló: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("esperaba 503 sin CONTROL_TOKEN, obtuve %d", resp.StatusCode)
	}
}

func TestWSRequiresDeviceKey(t *testing.T) {
	ts := httptest.NewServer(newTestServer("clave-placa"))
	defer ts.Close()

	// Sin clave: debe rechazarse el upgrade.
	if _, _, err := connectFakeESP32(t, ts.URL, ""); err == nil {
		t.Fatal("esperaba rechazo sin clave de dispositivo")
	}

	// Con clave correcta: debe conectar.
	conn, _, err := connectFakeESP32(t, ts.URL, "?key=clave-placa")
	if err != nil {
		t.Fatalf("conexión con clave correcta falló: %v", err)
	}
	conn.Close()
}

func TestWSRejectsBrowserOrigin(t *testing.T) {
	ts := httptest.NewServer(newTestServer(""))
	defer ts.Close()

	// Simulamos un navegador enviando Origin: debe rechazarse (anti-CSWSH).
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	header := http.Header{"Origin": {"https://evil.example"}}
	if _, _, err := websocket.DefaultDialer.Dial(wsURL, header); err == nil {
		t.Fatal("esperaba rechazo de conexión con cabecera Origin")
	}
}
