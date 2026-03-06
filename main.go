package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/bwmarrin/discordgo"
	bot "github.com/ritehand/asagumo"
)

const (
	optionNameSenkyoku = `選挙区`
	optionNameDuration = `全体時間`
)

func main() {
	s, err := discordgo.New("Bot " + bot.Token)
	if err != nil {
		log.Fatalln(err)
	}

	s.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		switch i.Type {
		case discordgo.InteractionApplicationCommand:
			name := i.ApplicationCommandData().Name
			switch name {
			case "senkyoku":
				handleSenkyokuCommand(s, i)
				// case "timer":
				// 	handleTimerCommand(s, i)
				// case "stop_timer":
				// 	handleStopTimerCommand(s, i)
			}
		}
	})

	// Voice state updates (join/leave/move) — detect immediate joins
	// s.AddHandler(func(s *discordgo.Session, vs *discordgo.VoiceStateUpdate) {
	// 	timerManager.HandleVoiceStateUpdate(s, vs)
	// })

	if err := s.Open(); err != nil {
		log.Fatalln(err)
	}
	defer s.Close()

	// Register slash commands
	commands := []*discordgo.ApplicationCommand{
		{
			Name:        "senkyoku",
			Description: "選挙区を選択してロールを付与します",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        optionNameSenkyoku,
					Description: "例：1区の場合「1」または「1区」を入力",
					Required:    true,
				},
			},
		},
		// {
		// 	Name:        "timer",
		// 	Description: "ボイスチャンネルの持ち時間制限を開始します（例: /timer duration:30m）",
		// 	Options: []*discordgo.ApplicationCommandOption{
		// 		{
		// 			Type:        discordgo.ApplicationCommandOptionString,
		// 			Name:        optionNameDuration,
		// 			Description: "合計持ち時間（例: 30m, 1h）",
		// 			Required:    true,
		// 		},
		// 	},
		// },
		// {
		// 	Name:        "stop_timer",
		// 	Description: "ボイスチャンネルの持ち時間制限を終了します",
		// },
	}

	if _, err := s.ApplicationCommandBulkOverwrite(s.State.User.ID, bot.GuildID, commands); err != nil {
		log.Fatalln(err)
	}

	// THE "KEEP-ALIVE" SERVER
	// Koyeb requires a health check to keep the service "Active."
	// We run this in a separate goroutine so it doesn't block the bot.
	go func() {
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "Bot is healthy!")
		})

		// Koyeb uses port 8000 by default
		port := os.Getenv("PORT")
		if port == "" {
			port = "8000"
		}

		fmt.Printf("Health check server listening on port %s\n", port)
		if err := http.ListenAndServe(":"+port, nil); err != nil {
			fmt.Printf("Health check server failed: %s\n", err)
		}
	}()

	// 5. Wait for a signal to quit
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
}

func sendEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}
