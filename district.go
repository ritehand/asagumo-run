package main

import (
	"fmt"
	"regexp"

	"github.com/bwmarrin/discordgo"
	bot "github.com/ritehand/asagumo"
)

var (
	districtRolePattern = regexp.MustCompile(`^[0-9]+区$`)

	// prefectureDistricts maps prefecture names to their number of election districts.
	// Data based on the 2022 redistribution (10-increase, 10-decrease) applied to the 2024/2025 elections.
	prefectureDistricts = map[string]int{
		"北海道": 12, "青森県": 3, "岩手県": 3, "宮城県": 5, "秋田県": 3, "山形県": 3, "福島県": 4,
		"茨城県": 7, "栃木県": 5, "群馬県": 5, "埼玉県": 16, "千葉県": 14, "東京都": 30, "神奈川県": 20,
		"新潟県": 5, "富山県": 3, "石川県": 3, "福井県": 2, "山梨県": 2, "長野県": 5, "岐阜県": 5,
		"静岡県": 8, "愛知県": 16, "三重県": 4, "滋賀県": 3, "京都府": 6, "大阪府": 19, "兵庫県": 12,
		"奈良県": 3, "和歌山県": 2, "鳥取県": 2, "島根県": 2, "岡山県": 4, "広島県": 6, "山口県": 3,
		"徳島県": 2, "香川県": 3, "愛媛県": 3, "高知県": 2, "福岡県": 11, "佐賀県": 2, "長崎県": 3,
		"熊本県": 4, "大分県": 3, "宮崎県": 3, "鹿児島県": 4, "沖縄県": 4,
	}
)

func handleSenkyokuCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options
	for _, opt := range options {
		if opt.Name == optionNameSenkyoku {
			input := opt.StringValue()
			targetDistNum, ok := bot.NormalizeNumber(input)
			if !ok || targetDistNum == 0 {
				sendEphemeral(s, i, "有効な数字が見つかりませんでした。「1」「1区」「一区」のように入力してください。")
				return
			}
			handleDistrictSelection(s, i, targetDistNum)
			return
		}
	}
}

func handleDistrictSelection(s *discordgo.Session, i *discordgo.InteractionCreate, targetDistNum int) {
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
		if _, ok := prefectureDistricts[roleName]; ok {
			userPref = roleName
			break
		}
	}

	if userPref == "" {
		sendEphemeral(s, i, "都道府県ロールが付与されていません。")
		return
	}

	// Check district count for this prefecture from static map
	districtCount, ok := prefectureDistricts[userPref]
	if !ok {
		sendEphemeral(s, i, fmt.Sprintf("エラー：%sの選挙区情報が見つかりませんでした。", userPref))
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
