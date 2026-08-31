package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
	"github.com/mmcdole/gofeed"
	"github.com/ritehand/asagumo-run/rss"
	"golang.org/x/net/html"
)

const (
	discordNewsWebhookEnv = "DISCORD_NEWS_WEBHOOK_URL"
	discordNewsForumChEnv = "DISCORD_NEWS_FORUM_CHANNEL_ID"
)

var discordNewsClient = &http.Client{Timeout: 10 * time.Second}

var (
	forumTagsMu sync.Mutex
	// forumTagIDs maps a forum tag name to its tag ID in the news forum channel.
	forumTagIDs = map[string]string{}
)

// forumChannelID resolves and validates DISCORD_NEWS_FORUM_CHANNEL_ID.
func forumChannelID() (snowflake.ID, error) {
	chID := strings.TrimSpace(os.Getenv(discordNewsForumChEnv))
	if chID == "" {
		return 0, fmt.Errorf("%s is not set", discordNewsForumChEnv)
	}
	channelID, err := snowflake.Parse(chID)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", discordNewsForumChEnv, chID, err)
	}
	return channelID, nil
}

// loadForumTags fetches the existing tags of the forum channel given by
// DISCORD_NEWS_FORUM_CHANNEL_ID into the name->ID cache. Called once at
// startup; tags are never fetched or created dynamically afterwards.
func loadForumTags(ctx context.Context, client bot.Client) error {
	channelID, err := forumChannelID()
	if err != nil {
		return err
	}

	forumTagsMu.Lock()
	defer forumTagsMu.Unlock()
	forumTagIDs, err = fetchForumTagIDs(ctx, client, channelID)
	if err != nil {
		return fmt.Errorf("forum channel %s: %w", channelID, err)
	}
	return nil
}

// fetchForumTagIDs returns the current name->ID map of the channel's tags.
func fetchForumTagIDs(ctx context.Context, client bot.Client, channelID snowflake.ID) (map[string]string, error) {
	ch, err := client.Rest.GetChannel(channelID)
	if err != nil {
		return nil, fmt.Errorf("get forum channel: %w", err)
	}
	forum, ok := ch.(discord.GuildForumChannel)
	if !ok {
		return nil, fmt.Errorf("channel %s is not a forum channel", channelID)
	}
	ids := make(map[string]string, len(forum.AvailableTags))
	for _, t := range forum.AvailableTags {
		ids[t.Name] = t.ID.String()
	}
	return ids, nil
}

type webhookExecutePayload struct {
	Username    string         `json:"username,omitempty"`
	AvatarURL   string         `json:"avatar_url,omitempty"`
	ThreadName  string         `json:"thread_name,omitempty"` // required for forum channels
	AppliedTags []string       `json:"applied_tags,omitempty"`
	Content     string         `json:"content,omitempty"`
	Embeds      []webhookEmbed `json:"embeds,omitempty"`
}

type webhookEmbed struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty"`
	Timestamp   string `json:"timestamp,omitempty"`
}

// faviconURL returns a favicon URL for the feed's site via Google's favicon service.
func faviconURL(feedURL string) string {
	u, err := url.Parse(feedURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return fmt.Sprintf("https://www.google.com/s2/favicons?domain=%s&sz=64", u.Hostname())
}

// stripHTML removes tags and collapses whitespace from an HTML fragment.
func stripHTML(s string) string {
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return s
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		if n.Type != html.ElementNode || (n.Data != "script" && n.Data != "style") {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
		}
	}
	walk(doc)
	return strings.Join(strings.Fields(b.String()), " ")
}

// postNewsToForum posts a new feed item to the Discord forum channel via webhook.
// Each post creates a new thread named after the article.
func postNewsToForum(ctx context.Context, client bot.Client, feed rss.FeedConfig, feedTitle string, item *gofeed.Item) {
	webhookURL := os.Getenv(discordNewsWebhookEnv)
	if webhookURL == "" {
		log.Printf("[discord-news] %s is not set, skipping %q", discordNewsWebhookEnv, item.Title)
		return
	}

	// Poster name: feed title, falling back to the site host.
	name := strings.TrimSpace(feedTitle)
	if name == "" {
		if u, err := url.Parse(feed.URL); err == nil {
			name = u.Hostname()
		}
	}

	payload := webhookExecutePayload{
		Username:   name,
		AvatarURL:  faviconURL(feed.URL),
		ThreadName: item.Title,
		Content:    item.Link,
		Embeds: []webhookEmbed{{
			Title: item.Title,
			URL:   item.Link,
		}},
	}
	if item.Description != "" {
		desc := stripHTML(item.Description)
		if len(desc) > 300 {
			desc = desc[:300] + "…"
		}
		payload.Embeds[0].Description = desc
	}
	if item.PublishedParsed != nil {
		payload.Embeds[0].Timestamp = item.PublishedParsed.Format(time.RFC3339)
	}
	// Tag comes from the feed config; the tag must already exist in the
	// forum channel. No tags are fetched or created dynamically.
	if name := strings.TrimSpace(feed.Tag); name != "" {
		forumTagsMu.Lock()
		id, ok := forumTagIDs[name]
		forumTagsMu.Unlock()
		if !ok {
			log.Printf("[discord-news] forum tag %q not found in channel, posting without tag", name)
		} else {
			payload.AppliedTags = append(payload.AppliedTags, id)
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[discord-news] marshal error: %v", err)
		return
	}

	resp, err := discordNewsClient.Post(webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("[discord-news] post error: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("[discord-news] unexpected status: %d", resp.StatusCode)
	}
}
