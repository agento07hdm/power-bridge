package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

// --------------------------------------------------------------------------
// Shelly Gen-2 RPC over WebSocket  (/rpc)
//
// Protocol:
//   client → server: {"id":1,"src":"user_1","method":"Shelly.GetDeviceInfo","params":{}}
//   server → client: {"id":1,"src":"<device-id>","dst":"user_1","result":{...}}
//              or:   {"id":1,"src":"<device-id>","dst":"user_1","error":{"code":-105,"message":"Method not found"}}
//
// Additionally, the server pushes NotifyStatus frames whenever a new
// poweropti reading is available (via RunNotifyBroadcaster):
//   server → client: {"src":"<device-id>","method":"NotifyStatus","params":{"ts":1234.5,"em:0":{...}}}
// --------------------------------------------------------------------------

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type wsRequest struct {
	ID     int64           `json:"id"`
	Src    string          `json:"src"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type wsResponse struct {
	ID     int64    `json:"id"`
	Src    string   `json:"src"`
	Dst    string   `json:"dst"`
	Result any      `json:"result,omitempty"`
	Error  *wsError `json:"error,omitempty"`
}

type wsError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpcDispatch maps a Shelly RPC method name to the result it should return.
// params is the raw JSON params object from the request (may be nil).
func (s *Server) rpcDispatch(method string, params json.RawMessage) (any, error) {
	switch method {
	case "Shelly.GetDeviceInfo":
		return s.buildDeviceInfo(), nil
	case "Shelly.GetStatus":
		return s.buildShellyStatus(), nil
	case "Shelly.GetConfig":
		return s.buildShellyConfig(), nil
	case "Shelly.GetComponents":
		return s.buildShellyComponents(), nil
	case "EM.GetStatus":
		return s.buildEMStatus(), nil
	case "EMData.GetStatus":
		if len(params) > 0 {
			var p struct {
				ID int `json:"id"`
			}
			if err := json.Unmarshal(params, &p); err == nil && p.ID != 0 {
				return nil, fmt.Errorf("component not found: emdata:%d", p.ID)
			}
		}
		return s.buildEMDataStatus(), nil
	case "Sys.GetStatus":
		return s.buildSysStatus(), nil
	case "Sys.GetConfig":
		return s.buildSysConfig(), nil
	default:
		return nil, fmt.Errorf("method not found: %s", method)
	}
}

func (s *Server) rpcWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade error: %v", err)
		return
	}

	deviceSrc := shellyID(s.cfg.ShellyMAC)

	// Each connection gets a buffered channel for outbound frames.
	// All writes go through this channel to guarantee single-writer semantics.
	sendCh := make(chan []byte, 32)
	s.hub.register(sendCh)
	defer func() {
		s.hub.unregister(sendCh)
		close(sendCh)
		conn.Close()
	}()

	// Writer goroutine – the only goroutine allowed to call conn.Write*.
	go func() {
		for data := range sendCh {
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				// Trigger read-loop exit by closing the underlying conn.
				conn.Close()
				return
			}
		}
	}()

	// Reader loop – handles incoming RPC requests.
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("ws read error: %v", err)
			}
			return
		}

		var req wsRequest
		if err := json.Unmarshal(msg, &req); err != nil {
			log.Printf("ws invalid JSON: %v", err)
			continue
		}

		result, dispatchErr := s.rpcDispatch(req.Method, req.Params)

		resp := wsResponse{
			ID:  req.ID,
			Src: deviceSrc,
			Dst: req.Src,
		}
		if dispatchErr != nil {
			resp.Error = &wsError{Code: -105, Message: dispatchErr.Error()}
		} else {
			resp.Result = result
		}

		data, err := json.Marshal(resp)
		if err != nil {
			log.Printf("ws marshal error: %v", err)
			continue
		}
		select {
		case sendCh <- data:
		default:
			log.Printf("ws send buffer full, dropping RPC response for method %s", req.Method)
		}
	}
}
