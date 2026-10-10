package alertbridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Handler struct {
	Token, ChatID, BaseURL string
	Client                 *http.Client
}
type notification struct {
	Status string `json:"status"`
	Alerts []struct {
		Status      string            `json:"status"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	} `json:"alerts"`
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.Token == "" || h.ChatID == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var payload notification
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload); err != nil {
		http.Error(w, "invalid notification", http.StatusBadRequest)
		return
	}
	var b strings.Builder
	for _, a := range payload.Alerts {
		if a.Labels["severity"] != "critical" {
			continue
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n", a.Status, a.Labels["alertname"], a.Annotations["summary"])
	}
	if b.Len() == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	text := b.String()
	if len(text) > 3500 {
		text = text[:3500] + "\n... additional alerts omitted"
	}
	body, err := json.Marshal(map[string]string{"chat_id": h.ChatID, "text": text})
	if err != nil {
		http.Error(w, "encoding failed", http.StatusInternalServerError)
		return
	}
	base := h.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, base+"/bot"+h.Token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		http.Error(w, "delivery failed", http.StatusBadGateway)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "delivery failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, "delivery failed", http.StatusBadGateway)
		return
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result); err != nil || !result.OK {
		http.Error(w, "delivery failed", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
