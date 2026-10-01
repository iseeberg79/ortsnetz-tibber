// ortsnetz-tibber liest die Phasenspannungen eines Tibber-Pulse per Websocket und
// meldet sie alle fünf Minuten an https://www.ortsnetz-auslastung.de.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	tibberAPI          = "https://api.tibber.com/v1-beta/gql"
	tibberWS           = "wss://websocket-api.tibber.com/v1-beta/gql/subscriptions"
	integrationVersion = "tibber-0.1.0"
	smartmeterModel    = "Tibber Pulse"
	minVolt, maxVolt   = 150, 300
	interval           = 5 * time.Minute
	reconnectDelay     = 30 * time.Second
	readTimeout        = 90 * time.Second
)

type liveMeasurement struct {
	Timestamp string   `json:"timestamp"`
	V1        *float64 `json:"voltagePhase1"`
	V2        *float64 `json:"voltagePhase2"`
	V3        *float64 `json:"voltagePhase3"`
}

type wsMessage struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type measurement struct {
	ObservedAt         string  `json:"observed_at"`
	Latitude           float64 `json:"latitude"`
	Longitude          float64 `json:"longitude"`
	L1V                float64 `json:"l1_v"`
	L2V                float64 `json:"l2_v"`
	L3V                float64 `json:"l3_v"`
	SmartmeterModel    string  `json:"smartmeter_model"`
	IntegrationVersion string  `json:"integration_version"`
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s fehlt", key)
	}
	return v
}

func mustFloat(key string) float64 {
	f, err := strconv.ParseFloat(mustEnv(key), 64)
	if err != nil {
		log.Fatalf("%s ist keine Zahl", key)
	}
	return f
}

// round3 rastert Koordinaten auf ca. 100 m (3 Nachkommastellen).
func round3(f float64) float64 { return math.Round(f*1000) / 1000 }

func inRange(v float64) bool { return v >= minVolt && v <= maxVolt }

// toMeasurement übernimmt L1 als Pflichtwert. L2/L3 fehlen bei einphasigen
// Anschlüssen und werden dann als -1 gemeldet.
func toMeasurement(m liveMeasurement, lat, lon float64, received time.Time) (measurement, bool) {
	if m.V1 == nil || !inRange(*m.V1) {
		return measurement{}, false
	}
	out := measurement{
		ObservedAt:         received.UTC().Format(time.RFC3339),
		Latitude:           lat,
		Longitude:          lon,
		L1V:                *m.V1,
		L2V:                -1,
		L3V:                -1,
		SmartmeterModel:    smartmeterModel,
		IntegrationVersion: integrationVersion,
	}
	if t, err := time.Parse(time.RFC3339, m.Timestamp); err == nil {
		out.ObservedAt = t.UTC().Format(time.RFC3339)
	}
	for i, p := range []*float64{m.V2, m.V3} {
		if p == nil {
			continue
		}
		if !inRange(*p) {
			return measurement{}, false
		}
		if i == 0 {
			out.L2V = *p
		} else {
			out.L3V = *p
		}
	}
	return out, true
}

// singleHomeID fragt die Homes des Tokens ab und liefert die ID, wenn es genau eines gibt.
func singleHomeID(ctx context.Context, token string) (string, error) {
	body := `{"query":"{ viewer { homes { id } } }"}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tibberAPI, bytes.NewReader([]byte(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ortsnetz-tibber/"+integrationVersion)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		Data struct {
			Viewer struct {
				Homes []struct {
					ID string `json:"id"`
				} `json:"homes"`
			} `json:"viewer"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", fmt.Errorf("tibber-api (HTTP %d): %w", resp.StatusCode, err)
	}
	homes := r.Data.Viewer.Homes
	if len(homes) != 1 {
		return "", fmt.Errorf("token hat %d Homes, TIBBER_HOME_ID setzen: %v", len(homes), homes)
	}
	return homes[0].ID, nil
}

// stream verbindet sich mit Tibber und ruft onMeasurement für jede Live-Messung auf,
// bis die Verbindung abbricht.
func stream(ctx context.Context, token, homeID string, onMeasurement func(liveMeasurement)) error {
	header := http.Header{"User-Agent": {"ortsnetz-tibber/" + integrationVersion}}
	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}, HandshakeTimeout: 15 * time.Second}
	conn, _, err := dialer.DialContext(ctx, tibberWS, header)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	initPayload, _ := json.Marshal(map[string]string{"token": token})
	if err := conn.WriteJSON(wsMessage{Type: "connection_init", Payload: initPayload}); err != nil {
		return err
	}

	query := fmt.Sprintf(`subscription { liveMeasurement(homeId: %q) { timestamp voltagePhase1 voltagePhase2 voltagePhase3 } }`, homeID)
	subPayload, _ := json.Marshal(map[string]string{"query": query})

	for {
		conn.SetReadDeadline(time.Now().Add(readTimeout))
		var msg wsMessage
		if err := conn.ReadJSON(&msg); err != nil {
			return err
		}
		switch msg.Type {
		case "connection_ack":
			if err := conn.WriteJSON(wsMessage{ID: "1", Type: "subscribe", Payload: subPayload}); err != nil {
				return err
			}
		case "ping":
			if err := conn.WriteJSON(wsMessage{Type: "pong"}); err != nil {
				return err
			}
		case "next":
			var p struct {
				Data struct {
					LiveMeasurement *liveMeasurement `json:"liveMeasurement"`
				} `json:"data"`
			}
			if err := json.Unmarshal(msg.Payload, &p); err == nil && p.Data.LiveMeasurement != nil {
				onMeasurement(*p.Data.LiveMeasurement)
			}
		case "error", "complete":
			return fmt.Errorf("tibber hat die Subscription beendet (%s): %s", msg.Type, msg.Payload)
		}
	}
}

func main() {
	var (
		token  = mustEnv("TIBBER_TOKEN")
		homeID = os.Getenv("TIBBER_HOME_ID")
		lat    = round3(mustFloat("LATITUDE"))
		lon    = round3(mustFloat("LONGITUDE"))
		apiURL = "https://www.ortsnetz-auslastung.de/v1/measurements"
	)
	if v := os.Getenv("API_URL"); v != "" {
		if _, err := url.ParseRequestURI(v); err != nil {
			log.Fatalf("API_URL ungültig: %v", err)
		}
		apiURL = v
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if homeID == "" {
		var err error
		if homeID, err = singleHomeID(ctx, token); err != nil {
			log.Fatalf("home-id: %v", err)
		}
	}

	var (
		mu       sync.Mutex
		latest   measurement
		latestAt time.Time
		ready    = make(chan struct{}, 1)
	)

	go func() {
		for {
			err := stream(ctx, token, homeID, func(m liveMeasurement) {
				now := time.Now()
				out, ok := toMeasurement(m, lat, lon, now)
				if !ok {
					return
				}
				mu.Lock()
				latest, latestAt = out, now
				mu.Unlock()
				select {
				case ready <- struct{}{}:
				default:
				}
			})
			if ctx.Err() != nil {
				return
			}
			log.Printf("tibber: %v; neuer Versuch in %s", err, reconnectDelay)
			select {
			case <-time.After(reconnectDelay):
			case <-ctx.Done():
				return
			}
		}
	}()

	client := &http.Client{Timeout: 15 * time.Second}
	send := func() {
		mu.Lock()
		m, at := latest, latestAt
		mu.Unlock()
		if time.Since(at) > interval {
			log.Printf("kein aktueller Messwert, überspringe")
			return
		}
		body, _ := json.Marshal(m)
		resp, err := client.Post(apiURL, "application/json", bytes.NewReader(body))
		if err != nil {
			log.Printf("senden: %v", err)
			return
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		switch resp.StatusCode {
		case http.StatusAccepted:
			log.Printf("ok L1=%.1f L2=%.1f L3=%.1f V: %s", m.L1V, m.L2V, m.L3V, respBody)
		case http.StatusForbidden:
			log.Fatalf("Standort gesperrt (403): %s", respBody)
		default:
			log.Printf("abgelehnt (%d): %s", resp.StatusCode, respBody)
		}
	}

	select {
	case <-ready:
	case <-ctx.Done():
		return
	}
	send()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			send()
		case <-ctx.Done():
			return
		}
	}
}
