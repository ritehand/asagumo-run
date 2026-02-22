package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"syscall"

	bot "github.com/1l0/asagumo" // TODO: move to the new one
	"github.com/bwmarrin/discordgo"
)

var (
	districtRolePattern = regexp.MustCompile(`^[0-9]+区$`)
	prefectures         = []string{
		"北海道", "青森県", "岩手県", "宮城県", "秋田県", "山形県", "福島県",
		"茨城県", "栃木県", "群馬県", "埼玉県", "千葉県", "神奈川県", "山梨県",
		"東京都", "新潟県", "富山県", "石川県", "福井県", "長野県", "岐阜県",
		"静岡県", "愛知県", "三重県", "滋賀県", "京都府", "大阪府", "兵庫県",
		"奈良県", "和歌山県", "鳥取県", "島根県", "岡山県", "広島県", "山口県",
		"徳島県", "香川県", "愛媛県", "高知県", "福岡県", "佐賀県", "長崎県",
		"熊本県", "大分県", "宮崎県", "鹿児島県", "沖縄県",
	}
)

func main() {
	db, err := bot.InitDB()
	if err != nil {
		log.Fatalln("Failed to initialize database:", err)
	}
	defer db.Close()

	s, err := discordgo.New("Bot " + bot.Token)
	if err != nil {
		log.Fatalln(err)
	}

	s.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		switch i.Type {
		case discordgo.InteractionApplicationCommand:
			if i.ApplicationCommandData().Name == "senkyoku" {
				handleSenkyokuCommand(s, i, db)
			}
		}
	})

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
					Name:        "選挙区",
					Description: "例：1区の場合「1」または「1区」を入力",
					Required:    true,
				},
			},
		},
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

		// Koyeb uses port 8080 by default
		port := os.Getenv("PORT")
		if port == "" {
			port = "8080"
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

func handleSenkyokuCommand(s *discordgo.Session, i *discordgo.InteractionCreate, db *sql.DB) {
	options := i.ApplicationCommandData().Options
	for _, opt := range options {
		if opt.Name == "選挙区" {
			input := opt.StringValue()
			targetDistNum, ok := bot.NormalizeNumber(input)
			if !ok || targetDistNum == 0 {
				sendEphemeral(s, i, "有効な数字が見つかりませんでした。「1」「1区」「一区」のように入力してください。")
				return
			}
			handleDistrictSelection(s, i, db, targetDistNum)
			return
		}
	}
}

func handleDistrictSelection(s *discordgo.Session, i *discordgo.InteractionCreate, db *sql.DB, targetDistNum int) {
	userID := i.Member.User.ID

	// Get guild roles to map IDs to names
	roles, err := s.GuildRoles(i.GuildID)
	if err != nil {
		sendEphemeral(s, i, "エラー：サーバー情報の取得に失敗しました。")
		return
	}
	roleMap := make(map[string]string) // ID -> Name
	for _, r := range roles {
		roleMap[r.ID] = r.Name
	}

	// Identify user's prefecture role
	var userPref string
	for _, roleID := range i.Member.Roles {
		roleName := roleMap[roleID]
		for _, p := range prefectures {
			if roleName == p {
				userPref = p
				break
			}
		}
		if userPref != "" {
			break
		}
	}

	if userPref == "" {
		sendEphemeral(s, i, "都道府県ロールが付与されていません。")
		return
	}

	// Check district count for this prefecture
	var districtCount int
	err = db.QueryRow("SELECT district_count FROM prefectures WHERE name = ?", userPref).Scan(&districtCount)
	if err != nil {
		sendEphemeral(s, i, fmt.Sprintf("エラー：データベースから%sの情報を取得できませんでした。", userPref))
		return
	}

	if targetDistNum > districtCount {
		sendEphemeral(s, i, fmt.Sprintf("%sには%d区までしか存在しません（%d区を選択）。", userPref, districtCount, targetDistNum))
		return
	}

	// Update roles: Remove current district roles, Add new one
	targetRoleName := fmt.Sprintf("%d区", targetDistNum)
	var targetRoleID string
	for _, r := range roles {
		if r.Name == targetRoleName {
			targetRoleID = r.ID
			break
		}
	}

	if targetRoleID == "" {
		sendEphemeral(s, i, fmt.Sprintf("エラー：%sのロールが見つかりませんでした。", targetRoleName))
		return
	}

	// Remove existing district roles
	for _, roleID := range i.Member.Roles {
		roleName := roleMap[roleID]
		if districtRolePattern.MatchString(roleName) {
			s.GuildMemberRoleRemove(i.GuildID, userID, roleID)
		}
	}

	// Add new district role
	err = s.GuildMemberRoleAdd(i.GuildID, userID, targetRoleID)
	if err != nil {
		sendEphemeral(s, i, "エラー：ロールの付与に失敗しました。")
		return
	}

	sendEphemeral(s, i, fmt.Sprintf("%sの%sロールを付与しました。", userPref, targetRoleName))
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
