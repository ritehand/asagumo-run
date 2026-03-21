package main

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/handler"
	"github.com/disgoorg/snowflake/v2"
	"github.com/pquerna/otp/totp"
)

const (
	optionNameOTPCode = "コード"
	customIDStopOTP   = "/stop_otp"
)

// otpSecret はTOTPのシークレット（環境変数 OTP_SECRET から取得）。
var otpSecret = os.Getenv("OTP_SECRET")

// otpRoleID は認証成功時に付与するロールID（環境変数 OTP_ROLE_ID から取得）。
var otpRoleID = func() snowflake.ID {
	id, _ := snowflake.Parse(os.Getenv("OTP_ROLE_ID"))
	return id
}()

// activeOTPSessions はモデレーターごとの「表示中セッション」を管理する。
// OTPコード自体はTOTPで生成するのでステートレス。
// セッションは「どのロールを付与するか」と「停止チャネル」のみ保持。
var activeOTPSessions = &otpSessionStore{sessions: make(map[snowflake.ID]*otpDisplaySession)}

type otpDisplaySession struct {
	stopCh chan struct{}
}

type otpSessionStore struct {
	mu       sync.Mutex
	sessions map[snowflake.ID]*otpDisplaySession
}

func (s *otpSessionStore) set(modID snowflake.ID, sess *otpDisplaySession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[modID] = sess
}

func (s *otpSessionStore) get(modID snowflake.ID) (*otpDisplaySession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[modID]
	return sess, ok
}

func (s *otpSessionStore) delete(modID snowflake.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, modID)
}

// moderatorRoleIDs はgen_otpを実行できるロールID一覧。
var moderatorRoleIDs = map[snowflake.ID]struct{}{
	snowflake.ID(1473436141884801155): {}, // モデレーター
	snowflake.ID(1473438169792778283): {}, // admin
	snowflake.ID(1473459256392028251): {}, // 管理人
}

func hasModerationRole(client *bot.Client, guildID snowflake.ID, member discord.ResolvedMember) bool {
	// Administratorパーミッションを持つロールがあれば許可
	for _, roleID := range member.RoleIDs {
		if r, ok := client.Caches.Role(guildID, roleID); ok {
			if r.Permissions.Has(discord.PermissionAdministrator) {
				return true
			}
		}
	}
	// モデレーター系ロールチェック
	for _, roleID := range member.RoleIDs {
		if _, ok := moderatorRoleIDs[roleID]; ok {
			return true
		}
	}
	return false
}
func currentOTP() (string, error) {
	return totp.GenerateCode(otpSecret, time.Now())
}

// validateOTP はコードを検証し、アクティブなセッションが存在すればtrueを返す。
func validateOTP(code string) bool {
	if !totp.Validate(code, otpSecret) {
		return false
	}
	activeOTPSessions.mu.Lock()
	defer activeOTPSessions.mu.Unlock()
	return len(activeOTPSessions.sessions) > 0
}

// formatOTPDisplay はOTPを大きく見やすく表示する。
func formatOTPDisplay(code string) string {
	wide := ""
	for _, c := range code {
		wide += string(rune('０' + (c - '0')))
	}
	return fmt.Sprintf("# %s", wide)
}

// secondsUntilNextPeriod は次のTOTP更新まで何秒かを返す。
func secondsUntilNextPeriod() time.Duration {
	now := time.Now().Unix()
	return time.Duration(30-now%30) * time.Second
}

func commandGenOTP(e *events.ApplicationCommandInteractionCreate) {
	if e.Member() == nil || !hasModerationRole(e.Client(), *e.GuildID(), *e.Member()) {
		sendEphemeral(e, "このコマンドはモデレーター以上のロールが必要です。")
		return
	}
	if otpSecret == "" {
		sendEphemeral(e, "OTP_SECRET 環境変数が設定されていません。")
		return
	}
	if otpRoleID == 0 {
		sendEphemeral(e, "OTP_ROLE_ID 環境変数が設定されていません。")
		return
	}

	modID := e.User().ID

	// 既存セッションがあれば停止
	if existing, exists := activeOTPSessions.get(modID); exists {
		close(existing.stopCh)
		activeOTPSessions.delete(modID)
	}

	sess := &otpDisplaySession{
		stopCh: make(chan struct{}),
	}
	activeOTPSessions.set(modID, sess)

	if err := e.DeferCreateMessage(true); err != nil {
		slog.Error("gen_otp: defer failed", "error", err)
		return
	}

	go runOTPDisplay(e, sess, modID)
}

func runOTPDisplay(e *events.ApplicationCommandInteractionCreate, sess *otpDisplaySession, modID snowflake.ID) {
	updateOTPMessage(e, sess)

	for {
		// 次のTOTP周期の切り替わりまで待つ
		wait := secondsUntilNextPeriod()
		select {
		case <-sess.stopCh:
			stopMsg := "OTP表示を終了しました。"
			empty := []discord.LayoutComponent{}
			_, _ = e.Client().Rest.UpdateInteractionResponse(
				e.ApplicationID(), e.Token(),
				discord.MessageUpdate{Content: &stopMsg, Components: &empty},
			)
			activeOTPSessions.delete(modID)
			return
		case <-time.After(wait):
			updateOTPMessage(e, sess)
		}
	}
}

func updateOTPMessage(e *events.ApplicationCommandInteractionCreate, _ *otpDisplaySession) {
	code, err := currentOTP()
	if err != nil {
		slog.Error("gen_otp: failed to generate OTP", "error", err)
		return
	}

	remaining := secondsUntilNextPeriod()
	content := fmt.Sprintf("**OTP** (%d秒ごとに更新)\n%s", int(remaining.Seconds()), formatOTPDisplay(code))
	stopButton := discord.NewPrimaryButton("終了", customIDStopOTP).WithStyle(discord.ButtonStyleDanger)
	components := []discord.LayoutComponent{discord.NewActionRow(stopButton)}

	_, _ = e.Client().Rest.UpdateInteractionResponse(
		e.ApplicationID(), e.Token(),
		discord.MessageUpdate{Content: &content, Components: &components},
	)
}

func commandOTP(e *events.ApplicationCommandInteractionCreate) {
	if otpSecret == "" {
		sendEphemeral(e, "OTP_SECRET 環境変数が設定されていません。")
		return
	}

	data := e.SlashCommandInteractionData()
	codeInt, ok := data.OptInt(optionNameOTPCode)
	if !ok {
		sendEphemeral(e, "コードを入力してください。")
		return
	}
	// 6桁ゼロパディング（例: 007341）
	code := fmt.Sprintf("%06d", codeInt)

	if !validateOTP(code) {
		sendEphemeral(e, "OTPが無効です。モデレーターに表示されているコードを確認してください。")
		return
	}

	userID := e.User().ID
	guildID := *e.GuildID()

	if err := e.Client().Rest.AddMemberRole(guildID, userID, otpRoleID); err != nil {
		slog.Error("otp: AddMemberRole failed", "error", err)
		sendEphemeral(e, "ロールの付与に失敗しました。")
		return
	}

	sendEphemeral(e, fmt.Sprintf("認証成功。<@&%s> ロールを付与しました。", otpRoleID))
}

func handleStopOTPButton(e *handler.ComponentEvent) error {
	modID := e.User().ID
	sess, exists := activeOTPSessions.get(modID)
	if !exists {
		return e.CreateMessage(discord.MessageCreate{
			Content: "アクティブなOTPセッションがありません。",
			Flags:   discord.MessageFlagEphemeral,
		})
	}

	close(sess.stopCh)
	activeOTPSessions.delete(modID)

	empty := []discord.LayoutComponent{}
	return e.UpdateMessage(discord.MessageUpdate{
		Content:    strPtr("OTP表示を終了しました。"),
		Components: &empty,
	})
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }
