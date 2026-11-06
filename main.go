package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

const serviceName = "eco-monitor-whatsapp-gateway"

type Config struct {
	Port              string
	DataDir           string
	APIToken          string
	AllowedRecipients map[string]struct{}
	DefaultRecipient  string
	AllowedOrigins    map[string]struct{}
	MinSendInterval   time.Duration
}

type Gateway struct {
	client    *whatsmeow.Client
	container *sqlstore.Container
	cfg       Config

	mu          sync.RWMutex
	state       string
	qrPNG       []byte
	qrUpdatedAt time.Time
	lastSentAt  time.Time
	sendMu      sync.Mutex
}

type sendRequest struct {
	To      string `json:"to"`
	Message string `json:"message"`
}

type apiResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Fatalf("create data directory: %v", err)
	}

	dbPath := filepath.Join(cfg.DataDir, "whatsmeow.db")
	dsn := fmt.Sprintf("file:%s?_foreign_keys=on&_busy_timeout=5000", filepath.ToSlash(dbPath))
	container, err := sqlstore.New(context.Background(), "sqlite3", dsn, waLog.Noop)
	if err != nil {
		log.Fatalf("initialize WhatsApp SQLite store: %v", err)
	}
	defer container.Close()

	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		log.Fatalf("load WhatsApp device store: %v", err)
	}

	waClient := whatsmeow.NewClient(deviceStore, waLog.Noop)
	gateway := &Gateway{
		client:    waClient,
		container: container,
		cfg:       cfg,
		state:     "starting",
	}
	gateway.registerWhatsAppEvents()

	if deviceStore.ID == nil {
		gateway.setState("waiting_for_qr")
		qrChannel, err := waClient.GetQRChannel(context.Background())
		if err != nil {
			log.Fatalf("create WhatsApp QR channel: %v", err)
		}
		if err := waClient.Connect(); err != nil {
			log.Fatalf("connect WhatsApp client for pairing: %v", err)
		}
		go gateway.consumeQRChannel(qrChannel)
		log.Println("WhatsApp is not paired yet; open /pair to scan the QR code")
	} else {
		gateway.setState("connecting")
		if err := waClient.Connect(); err != nil {
			log.Printf("initial WhatsApp reconnect returned an error: %v", err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", gateway.rootHandler)
	mux.HandleFunc("GET /healthz", gateway.healthHandler)
	mux.HandleFunc("GET /pair", gateway.pairPageHandler)
	mux.Handle("GET /api/status", gateway.authMiddleware(http.HandlerFunc(gateway.statusHandler)))
	mux.Handle("GET /api/qr.png", gateway.authMiddleware(http.HandlerFunc(gateway.qrHandler)))
	mux.Handle("POST /api/send", gateway.authMiddleware(http.HandlerFunc(gateway.sendHandler)))
	mux.HandleFunc("OPTIONS /api/", gateway.optionsHandler)

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           gateway.corsMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      75 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("%s listening on :%s; data directory: %s", serviceName, cfg.Port, cfg.DataDir)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("HTTP server: %v", err)
	}
}

func loadConfig() (Config, error) {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8080"
	}
	if _, err := strconv.Atoi(port); err != nil {
		return Config{}, fmt.Errorf("PORT must be a valid port number")
	}

	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir == "" {
		dataDir = "/data"
	}

	token := strings.TrimSpace(os.Getenv("API_TOKEN"))
	if len(token) < 32 {
		return Config{}, fmt.Errorf("API_TOKEN is required and must be at least 32 characters")
	}

	allowedRecipients := make(map[string]struct{})
	for _, value := range strings.Split(os.Getenv("ALLOWED_RECIPIENTS"), ",") {
		phone := normalizePhone(value)
		if phone != "" {
			allowedRecipients[phone] = struct{}{}
		}
	}

	defaultRecipient := normalizePhone(os.Getenv("DEFAULT_RECIPIENT"))
	if defaultRecipient != "" {
		if _, ok := allowedRecipients[defaultRecipient]; !ok {
			return Config{}, fmt.Errorf("DEFAULT_RECIPIENT must also be listed in ALLOWED_RECIPIENTS")
		}
	}

	allowedOrigins := make(map[string]struct{})
	for _, origin := range strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",") {
		origin = strings.TrimRight(strings.TrimSpace(origin), "/")
		if origin != "" {
			allowedOrigins[origin] = struct{}{}
		}
	}

	interval := 20 * time.Second
	if raw := strings.TrimSpace(os.Getenv("MIN_SEND_INTERVAL_SECONDS")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 1 || seconds > 3600 {
			return Config{}, fmt.Errorf("MIN_SEND_INTERVAL_SECONDS must be between 1 and 3600")
		}
		interval = time.Duration(seconds) * time.Second
	}

	return Config{
		Port:              port,
		DataDir:           dataDir,
		APIToken:          token,
		AllowedRecipients: allowedRecipients,
		DefaultRecipient:  defaultRecipient,
		AllowedOrigins:    allowedOrigins,
		MinSendInterval:   interval,
	}, nil
}

func (g *Gateway) registerWhatsAppEvents() {
	g.client.AddEventHandler(func(evt interface{}) {
		switch evt.(type) {
		case *events.Connected:
			g.setState("connected")
			g.clearQR()
			log.Println("WhatsApp connected and authenticated")
		case *events.Disconnected:
			g.setState("disconnected")
			log.Println("WhatsApp websocket disconnected; library will handle reconnect where possible")
		case *events.LoggedOut:
			g.setState("logged_out")
			g.clearQR()
			log.Println("WhatsApp session was logged out; a new pairing is required")
		case *events.PairSuccess:
			g.setState("pairing_completed_connecting")
			g.clearQR()
			log.Println("WhatsApp QR pairing completed; waiting for authenticated connection")
		case *events.PairError:
			g.setState("pairing_error")
			log.Println("WhatsApp pairing failed")
		}
	})
}

func (g *Gateway) consumeQRChannel(qrChannel <-chan whatsmeow.QRChannelItem) {
	for item := range qrChannel {
		switch item.Event {
		case "code":
			png, err := qrcode.Encode(item.Code, qrcode.Medium, 320)
			if err != nil {
				log.Printf("generate QR image: %v", err)
				continue
			}
			g.mu.Lock()
			g.qrPNG = png
			g.qrUpdatedAt = time.Now().UTC()
			g.state = "waiting_for_qr_scan"
			g.mu.Unlock()
		case "success":
			g.clearQR()
			g.setState("pairing_completed_connecting")
		case "timeout":
			g.clearQR()
			g.setState("qr_timeout_restart_required")
		case "error":
			g.clearQR()
			g.setState("pairing_error")
			if item.Error != nil {
				log.Printf("WhatsApp QR pairing error: %v", item.Error)
			}
		default:
			if item.Event != "" {
				log.Printf("WhatsApp QR channel event: %s", item.Event)
			}
		}
	}
}

func (g *Gateway) setState(state string) {
	g.mu.Lock()
	g.state = state
	g.mu.Unlock()
}

func (g *Gateway) clearQR() {
	g.mu.Lock()
	g.qrPNG = nil
	g.qrUpdatedAt = time.Time{}
	g.mu.Unlock()
}

func (g *Gateway) rootHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service": serviceName,
		"status":  "running",
		"health":  "/healthz",
		"pair":    "/pair",
	})
}

func (g *Gateway) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": serviceName})
}

func (g *Gateway) statusHandler(w http.ResponseWriter, r *http.Request) {
	g.mu.RLock()
	state := g.state
	qrAvailable := len(g.qrPNG) > 0
	qrUpdatedAt := g.qrUpdatedAt
	g.mu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"service":            serviceName,
		"state":              state,
		"whatsapp_connected": g.client.IsConnected(),
		"whatsapp_logged_in": g.client.IsLoggedIn(),
		"qr_available":       qrAvailable,
		"qr_updated_at":      optionalTime(qrUpdatedAt),
		"server_time":        time.Now().UTC().Format(time.RFC3339),
	})
}

func (g *Gateway) qrHandler(w http.ResponseWriter, r *http.Request) {
	g.mu.RLock()
	png := append([]byte(nil), g.qrPNG...)
	g.mu.RUnlock()
	if len(png) == 0 {
		writeJSON(w, http.StatusNotFound, apiResponse{OK: false, Error: "QR code is not available; check WhatsApp status or restart pairing"})
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}

func (g *Gateway) sendHandler(w http.ResponseWriter, r *http.Request) {
	if !g.client.IsConnected() || !g.client.IsLoggedIn() {
		writeJSON(w, http.StatusServiceUnavailable, apiResponse{OK: false, Error: "WhatsApp is not connected; pair the device first"})
		return
	}

	var req sendRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{OK: false, Error: "invalid JSON request"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, apiResponse{OK: false, Error: "request must contain a single JSON object"})
		return
	}

	phone := normalizePhone(req.To)
	if phone == "" {
		phone = g.cfg.DefaultRecipient
	}
	if phone == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{OK: false, Error: "recipient is required; use international format without +, spaces, or leading 0"})
		return
	}
	if _, allowed := g.cfg.AllowedRecipients[phone]; !allowed {
		writeJSON(w, http.StatusForbidden, apiResponse{OK: false, Error: "recipient is not in ALLOWED_RECIPIENTS"})
		return
	}

	message := strings.TrimSpace(req.Message)
	if message == "" || len([]rune(message)) > 1000 {
		writeJSON(w, http.StatusBadRequest, apiResponse{OK: false, Error: "message must contain 1 to 1000 characters"})
		return
	}

	g.sendMu.Lock()
	defer g.sendMu.Unlock()
	if !g.lastSentAt.IsZero() && time.Since(g.lastSentAt) < g.cfg.MinSendInterval {
		remaining := g.cfg.MinSendInterval - time.Since(g.lastSentAt)
		writeJSON(w, http.StatusTooManyRequests, apiResponse{OK: false, Error: fmt.Sprintf("send cooldown active; retry in about %d seconds", int(remaining.Seconds())+1)})
		return
	}

	jid, err := types.ParseJID(phone + "@s.whatsapp.net")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{OK: false, Error: "invalid WhatsApp recipient number"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	_, err = g.client.SendMessage(ctx, jid, &waE2E.Message{Conversation: proto.String(message)})
	if err != nil {
		log.Printf("WhatsApp message send failed: %v", err)
		writeJSON(w, http.StatusBadGateway, apiResponse{OK: false, Error: "WhatsApp could not acknowledge the message"})
		return
	}
	g.lastSentAt = time.Now()
	log.Printf("WhatsApp message accepted by server for configured recipient %s", maskPhone(phone))
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Message: "message accepted by WhatsApp server"})
}

func (g *Gateway) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			writeJSON(w, http.StatusUnauthorized, apiResponse{OK: false, Error: "missing bearer token"})
			return
		}
		provided := strings.TrimSpace(strings.TrimPrefix(auth, prefix))
		if len(provided) != len(g.cfg.APIToken) || subtle.ConstantTimeCompare([]byte(provided), []byte(g.cfg.APIToken)) != 1 {
			writeJSON(w, http.StatusUnauthorized, apiResponse{OK: false, Error: "invalid API token"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (g *Gateway) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")
		if origin != "" {
			if _, allowed := g.cfg.AllowedOrigins[origin]; allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Max-Age", "600")
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (g *Gateway) optionsHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (g *Gateway) pairPageHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, pairPageHTML)
}

func normalizePhone(value string) string {
	var builder strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if r >= '0' && r <= '9' {
			builder.WriteRune(r)
		}
	}
	phone := builder.String()
	if strings.HasPrefix(phone, "0") {
		phone = "62" + strings.TrimPrefix(phone, "0")
	}
	if len(phone) < 8 || len(phone) > 15 {
		return ""
	}
	return phone
}

func maskPhone(phone string) string {
	if len(phone) <= 4 {
		return "****"
	}
	return strings.Repeat("*", len(phone)-4) + phone[len(phone)-4:]
}

func optionalTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Format(time.RFC3339)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

const pairPageHTML = `<!doctype html>
<html lang="id">
<head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>ECO-MONITOR AI · WhatsApp Pairing</title>
<style>
:root{color-scheme:dark;font-family:Inter,system-ui,-apple-system,Segoe UI,sans-serif;background:#0b1220;color:#e8eef7}*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px;background:radial-gradient(circle at 20% 10%,#123b42 0,transparent 35%),#0b1220}.card{width:min(100%,470px);padding:28px;border:1px solid #253649;border-radius:22px;background:#111c2b;box-shadow:0 24px 80px #0007}h1{font-size:23px;margin:0 0 8px}.sub{color:#9db0c3;font-size:14px;line-height:1.6;margin:0 0 22px}.field{display:grid;gap:8px;margin-bottom:14px}label{font-size:13px;color:#c6d3df}input{width:100%;padding:13px 14px;border-radius:11px;border:1px solid #34495d;background:#0b1421;color:white;font-size:14px}button{border:0;border-radius:11px;padding:12px 16px;background:#19a784;color:#041b17;font-weight:700;cursor:pointer;width:100%}button:disabled{opacity:.5;cursor:wait}.qr{display:none;margin:22px auto 12px;padding:14px;background:#fff;border-radius:16px;width:fit-content}.qr img{display:block;width:260px;height:260px}.state{margin-top:14px;padding:12px;border-radius:11px;background:#0a1522;color:#b7c8d8;font-size:13px;line-height:1.5}.steps{font-size:13px;color:#9db0c3;line-height:1.7;margin:20px 0 0;padding-left:20px}.warn{font-size:12px;color:#e9bd76;margin-top:16px;line-height:1.6}
</style></head><body><main class="card"><h1>ECO-MONITOR AI</h1><p class="sub">Pairing WhatsApp Gateway. API Token tidak disimpan di URL. Masukkan token yang dibuat pada Environment Variables Koyeb.</p><div class="field"><label for="token">Gateway API Token</label><input id="token" type="password" autocomplete="off" placeholder="Tempel API_TOKEN dari Koyeb"></div><button id="connect">Periksa status & tampilkan QR</button><div class="qr" id="qrbox"><img id="qr" alt="WhatsApp pairing QR code"></div><div class="state" id="state">Belum terhubung. Masukkan token untuk memulai pemeriksaan.</div><ol class="steps"><li>Buka WhatsApp di ponsel.</li><li>Pilih Setelan / Settings → Perangkat tertaut / Linked devices.</li><li>Pilih Tautkan perangkat / Link a device.</li><li>Pindai QR yang muncul di halaman ini.</li></ol><p class="warn">Jangan bagikan API Token atau QR pairing kepada siapa pun. Setelah berhasil terhubung, halaman ini akan menampilkan status koneksi.</p></main>
<script>
const tokenInput=document.getElementById('token'),btn=document.getElementById('connect'),stateBox=document.getElementById('state'),qrBox=document.getElementById('qrbox'),qrImg=document.getElementById('qr');let timer=null,token='';
async function refresh(){try{const res=await fetch('/api/status',{headers:{Authorization:'Bearer '+token},cache:'no-store'});if(!res.ok)throw new Error('Status gagal ('+res.status+'). Periksa token.');const s=await res.json();stateBox.textContent='Status WhatsApp: '+s.state+' · Connected: '+s.whatsapp_connected+' · Logged in: '+s.whatsapp_logged_in;if(s.qr_available){const img=await fetch('/api/qr.png',{headers:{Authorization:'Bearer '+token},cache:'no-store'});if(img.ok){const blob=await img.blob();const old=qrImg.dataset.url;if(old)URL.revokeObjectURL(old);const url=URL.createObjectURL(blob);qrImg.src=url;qrImg.dataset.url=url;qrBox.style.display='block'}}else{qrBox.style.display='none'}if(s.whatsapp_logged_in){stateBox.textContent='Berhasil terhubung ke WhatsApp. Kamu bisa menutup halaman ini.';clearInterval(timer);timer=null}}catch(e){stateBox.textContent=e.message}}
btn.addEventListener('click',()=>{token=tokenInput.value.trim();if(token.length<32){stateBox.textContent='Token harus diisi dengan benar.';return}btn.disabled=true;refresh().finally(()=>{btn.disabled=false});if(timer)clearInterval(timer);timer=setInterval(refresh,2500)});
</script></body></html>`
