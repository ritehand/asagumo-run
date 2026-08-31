package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime/debug"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/handler"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"github.com/mmcdole/gofeed"
	bot_asagumo "github.com/ritehand/asagumo"
	"github.com/ritehand/asagumo-run/rss"
	"github.com/thomas-vilte/dave-go/session"
)

const (
	optionNameSenkyoku = `選挙区`
	optionNameDuration = `全体時間`
)

var version string

var feeds = []rss.FeedConfig{
	{Tag: "seifu", URL: "https://www.gov-online.go.jp/rss/index.rdf", Interval: 10 * time.Minute},
	{Tag: "kourou", URL: "https://www.mhlw.go.jp/stf/news.rdf", Interval: 10 * time.Minute},
	{Tag: "nhk", URL: "https://news.web.nhk/n-data/conf/na/rss/cat4.xml", Interval: 10 * time.Minute},
	{Tag: "soumu", URL: "https://www.soumu.go.jp/news.rdf", Interval: 10 * time.Minute},
	{Tag: "monka", URL: "https://www.mext.go.jp/b_menu/news/index.rdf", Interval: 10 * time.Minute},
	{Tag: "tosho", URL: "https://www.ndl.go.jp/rss/ndls/bureau-rss-all.xml", Interval: 10 * time.Minute},
	{Tag: "kantei", URL: "https://www.kantei.go.jp/index-jnews.rdf", Interval: 10 * time.Minute},
	{Tag: "houmu", URL: "https://www.moj.go.jp/info.xml", Interval: 10 * time.Minute},
	{Tag: "gaimu", URL: "https://www.anzen.mofa.go.jp/rss/news.xml", Interval: 10 * time.Minute},
	{Tag: "nousui", URL: "https://www.maff.go.jp/rss.xml", Interval: 10 * time.Minute},
	{Tag: "keisan", URL: "https://www.meti.go.jp/ml_index_release_atom.xml", Interval: 10 * time.Minute},
	{Tag: "egov", URL: "https://www.meti.go.jp/ml_index_release_atom.xml", Interval: 10 * time.Minute},
	{Tag: "kokkou", URL: "https://www.mlit.go.jp/pressrelease.rdf", Interval: 10 * time.Minute},
	{Tag: "kankyou", URL: "https://greenfinanceportal.env.go.jp/news.xml", Interval: 10 * time.Minute},
	{Tag: "bouei", URL: "https://www.mod.go.jp/j/rss/news.xml", Interval: 10 * time.Minute},
	{Tag: "digital", URL: "https://www.digital.go.jp/rss/news.xml", Interval: 10 * time.Minute},
}

func main() {
	if buildInfo, ok := debug.ReadBuildInfo(); ok {
		version = buildInfo.Main.Version
	}

	h := handler.New()
	h.Command("/senkyoku", func(e *handler.CommandEvent) error {
		handleSenkyokuCommand(e.ApplicationCommandInteractionCreate)
		return nil
	})
	h.Command("/timer", func(e *handler.CommandEvent) error {
		commandTimer(e.ApplicationCommandInteractionCreate)
		return nil
	})
	h.Command("/stop_timer", func(e *handler.CommandEvent) error {
		commandStopTimer(e.ApplicationCommandInteractionCreate)
		return nil
	})
	h.Command("/show_timer", func(e *handler.CommandEvent) error {
		commandShowTimer(e.ApplicationCommandInteractionCreate)
		return nil
	})
	h.Command("/gen_otp", func(e *handler.CommandEvent) error {
		commandGenOTP(e.ApplicationCommandInteractionCreate)
		return nil
	})
	h.Command("/otp", func(e *handler.CommandEvent) error {
		commandOTP(e.ApplicationCommandInteractionCreate)
		return nil
	})
	h.Component(customIDStopOTP, handleStopOTPButton)

	client, err := disgo.New(bot_asagumo.Token,
		bot.WithCacheConfigOpts(
			cache.WithCaches(
				cache.FlagVoiceStates,
				cache.FlagGuilds,
				cache.FlagChannels,
				cache.FlagRoles,
				cache.FlagMembers,
			),
		),
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(
				gateway.IntentGuilds,
				gateway.IntentGuildMembers,
				gateway.IntentGuildVoiceStates,
			),
		),
		bot.WithVoiceManagerConfigOpts(
			voice.WithDaveSessionCreateFunc(session.New),
			// voice.WithDaveSessionLogger(slog.Default()),
			// voice.WithConnConfigOpts(voice.WithConnGatewayConfigOpts(voice.WithGatewayAutoReconnect(true))),
		),
		bot.WithEventListeners(h),
		bot.WithEventListenerFunc(func(e *events.GuildVoiceStateUpdate) {
			timerManager.HandleVoiceStateUpdate(e.Client(), e)
		}),
		bot.WithLogger(slog.Default()),
	)
	if err != nil {
		slog.Error("Failed to create disgo client", "error", err)
		os.Exit(1)
	}

	if err := client.OpenGateway(context.Background()); err != nil {
		slog.Error("Failed to open gateway", "error", err)
		os.Exit(1)
	}
	defer client.Close(context.Background())

	// Load existing forum tags into the cache; missing ones are created
	// on demand when items are posted
	if err := loadForumTags(context.Background(), *client); err != nil {
		slog.Error("Failed to load forum tags", "error", err)
	}

	// Register slash commands
	commands := []discord.ApplicationCommandCreate{
		discord.SlashCommandCreate{
			Name:        "senkyoku",
			Description: "選挙区を選択してロールを付与します",
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionString{
					Name:        optionNameSenkyoku,
					Description: "例: 1区の場合「1」または「1区」を入力",
					Required:    true,
				},
			},
		},
		discord.SlashCommandCreate{
			Name:        "timer",
			Description: "タイマーを開始します",
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionString{
					Name:        optionNameDuration,
					Description: "例: 「30m」、「1h」、「40s」",
					Required:    true,
				},
			},
		},
		discord.SlashCommandCreate{
			Name:        "stop_timer",
			Description: "タイマーを終了します",
		},
		discord.SlashCommandCreate{
			Name:        "show_timer",
			Description: "残りの持ち時間を表示します",
		},
		discord.SlashCommandCreate{
			Name:        "gen_otp",
			Description: "OTPを生成・表示します（モデレーター専用）",
		},
		discord.SlashCommandCreate{
			Name:        "otp",
			Description: "OTPを入力してロールを取得します",
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionInt{
					Name:        optionNameOTPCode,
					Description: "モデレーターに表示されている6桁のコード",
					Required:    true,
					MinValue:    intPtr(0),
					MaxValue:    intPtr(999999),
				},
			},
		},
	}

	guildID, _ := snowflake.Parse(bot_asagumo.GuildID)
	if _, err := client.Rest.SetGuildCommands(client.ApplicationID, guildID, commands); err != nil {
		slog.Error("Failed to set guild commands", "error", err)
		os.Exit(1)
	}

	// THE "KEEP-ALIVE" SERVER
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Bot is healthy!")
	})

	// YouTube WebSub Webhook
	initWebSub()
	http.HandleFunc("/webhook", handleWebhook)

	onUpdate := func(feedConfig rss.FeedConfig, feedTitle string, item *gofeed.Item) {
		postNewsToForum(context.Background(), *client, feedConfig, feedTitle, item)
	}

	watcher := rss.NewWatcher(20, onUpdate) // 同時に叩きに行くのは最大20フィードまで

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go watcher.Run(ctx, feeds)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}
	fmt.Printf("keep-alive server listening on port %s\n", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		fmt.Printf("keep-alive server failed: %s\n", err)
	}
}

func sendEphemeral(e *events.ApplicationCommandInteractionCreate, content string) {
	err := e.CreateMessage(discord.MessageCreate{
		Content: content,
		Flags:   discord.MessageFlagEphemeral,
	})
	if err != nil {
		slog.Error("Failed to send ephemeral message", "error", err)
	}
}
