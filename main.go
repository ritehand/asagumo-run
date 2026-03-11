package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime/debug"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/handler"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave/golibdave"
	"github.com/disgoorg/snowflake/v2"
	bot_asagumo "github.com/ritehand/asagumo"
)

const (
	optionNameSenkyoku = `選挙区`
	optionNameDuration = `全体時間`
)

var version string

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
			voice.WithDaveSessionCreateFunc(golibdave.NewSession),
			voice.WithDaveSessionLogger(slog.Default()),
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
