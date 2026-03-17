package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var (
	websubCallbackURL string
	hubSecret         string
)

// YouTube channel IDs to subscribe
var channelIDsToSubscribe = []string{
	"UC7Opkf9V2DMmw9SXsOcSOeA",
}

// YouTube WebSub Atom feed
type Feed struct {
	XMLName xml.Name `xml:"feed"`
	Entries []Entry  `xml:"entry"`
}

type Entry struct {
	VideoID   string    `xml:"videoId"`
	ChannelID string    `xml:"channelId"`
	Title     string    `xml:"title"`
	Published time.Time `xml:"published"`
	Updated   time.Time `xml:"updated"`
	Link      struct {
		Href string `xml:"href,attr"`
	} `xml:"link"`
}

func initWebSub() {
	publicHost := os.Getenv("PUBLIC_HOST")
	hubSecret = os.Getenv("HUB_SECRET")
	if publicHost == "" || hubSecret == "" {
		log.Fatal("PUBLIC_HOST or HUB_SECRET is not set")
	}
	websubCallbackURL = publicHost + "/webhook"

	// サブスクリプション登録
	for _, id := range channelIDsToSubscribe {
		if err := subscribe(id); err != nil {
			log.Printf("subscribe failed for %s: %v", id, err)
		} else {
			log.Printf("subscribed: %s", id)
		}
	}
}

// サブスクリプションの有効期限はデフォルト10日なので、定期的に再登録が必要。
func subscribe(channelID string) error {
	const hubURL = "https://pubsubhubbub.appspot.com/subscribe"

	topicURL := "https://www.youtube.com/xml/feeds/videos.xml?channel_id=" + channelID

	form := url.Values{}
	form.Set("hub.callback", websubCallbackURL)
	form.Set("hub.mode", "subscribe")
	form.Set("hub.topic", topicURL)
	form.Set("hub.secret", hubSecret)
	form.Set("hub.lease_seconds", "864000") // 10日

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hubURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 202 Accepted が正常レスポンス
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, body)
	}
	return nil
}

// handleWebhook はHubからのGET（サブスクリプション確認）と
// POST（イベント通知）を処理する。
func handleWebhook(w http.ResponseWriter, r *http.Request) {
	switch r.Method {

	// GETはサブスクリプション確認。hub.challengeをそのまま返す。
	case http.MethodGet:
		challenge := r.URL.Query().Get("hub.challenge")
		if challenge == "" {
			http.Error(w, "missing hub.challenge", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, challenge)

	// POSTは実際のイベント通知。
	case http.MethodPost:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		defer r.Body.Close()

		// HMACシグネチャ検証
		if !verifySignature(r.Header.Get("X-Hub-Signature"), body) {
			http.Error(w, "invalid signature", http.StatusForbidden)
			return
		}

		// 202を先に返してHubへの応答を速やかに完了させる
		w.WriteHeader(http.StatusNoContent)

		// Atomフィードをパース
		var feed Feed
		if err := xml.Unmarshal(body, &feed); err != nil {
			log.Printf("xml parse error: %v", err)
			return
		}

		for _, entry := range feed.Entries {
			onNewEntry(entry)
		}

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// verifySignature はX-Hub-SignatureヘッダーのHMAC-SHA1を検証する。
func verifySignature(header string, body []byte) bool {
	if hubSecret == "" {
		return false
	}
	if !strings.HasPrefix(header, "sha1=") {
		return false
	}
	got := strings.TrimPrefix(header, "sha1=")

	mac := hmac.New(sha1.New, []byte(hubSecret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(got), []byte(expected))
}

// onNewEntry は新着エントリを受け取ったときの処理。
// ここにDiscord Webhook送信などを追加する。
func onNewEntry(entry Entry) {
	log.Printf("🆕 新着\n  title:   %s\n  videoID: %s\n  link:    %s\n  updated: %s",
		entry.Title,
		entry.VideoID,
		entry.Link.Href,
		entry.Updated.Format(time.RFC3339),
	)

	// TODO: Discord Botで通知を送る
}
