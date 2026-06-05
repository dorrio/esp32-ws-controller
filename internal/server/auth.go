package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// requireControlToken envuelve un handler exigiendo el token de control en la
// cabecera `Authorization: Bearer <token>`.
//
// El token Bearer cumple dos funciones de seguridad:
//   - Autenticación: sin él, 401. Nadie controla la placa sin conocerlo.
//   - Anti-CSRF: un navegador no adjunta esta cabecera en peticiones cross-site,
//     así que una web maliciosa no puede disparar comandos.
//
// Si CONTROL_TOKEN no está configurado, la API se considera deshabilitada y
// responde 503 (mejor fallar cerrado que quedar abierta a Internet).
func (s *Server) requireControlToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.controlToken == "" {
			writeError(w, http.StatusServiceUnavailable,
				"API de control deshabilitada: define la variable de entorno CONTROL_TOKEN")
			return
		}
		if !hasValidBearer(r, s.controlToken) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "no autorizado")
			return
		}
		next(w, r)
	}
}

// hasValidBearer comprueba la cabecera Authorization con comparación en tiempo
// constante para no filtrar el token por canal lateral de temporización.
func hasValidBearer(r *http.Request, token string) bool {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return false
	}
	got := strings.TrimPrefix(h, prefix)
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

// authorizeBoard valida la conexión de una placa al WebSocket antes del upgrade.
//
//   - Rechaza cualquier petición con cabecera Origin: el firmware real del ESP32
//     no la envía, pero un navegador sí. Esto bloquea el secuestro cross-site.
//   - Si DEVICE_KEY está configurada, exige `?key=<clave>` válida.
//
// Devuelve un mensaje de error (no vacío) si la conexión debe rechazarse.
func (s *Server) authorizeBoard(r *http.Request) string {
	if r.Header.Get("Origin") != "" {
		return "conexiones con cabecera Origin no permitidas"
	}
	if s.deviceKey == "" {
		return "" // sin clave configurada: se acepta (se avisa al arrancar)
	}
	key := r.URL.Query().Get("key")
	if subtle.ConstantTimeCompare([]byte(key), []byte(s.deviceKey)) != 1 {
		return "clave de dispositivo inválida"
	}
	return ""
}
