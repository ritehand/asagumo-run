package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"sync"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

const sampleRate = 48000 // 48kHz as Discord uses

var timerManager = &TimerManager{sessions: make(map[snowflake.ID]*TimerSession)}

func commandTimer(e *events.ApplicationCommandInteractionCreate) {
	snum := len(timerManager.sessions)
	slog.Info("commandTimer", "sessions", snum)
	data := e.SlashCommandInteractionData()
	input, _ := data.OptString(optionNameDuration)
	dur, err := time.ParseDuration(input)
	if err != nil {
		_ = e.CreateMessage(discord.MessageCreate{
			Content: "有効な時間を指定してください 例: 30m, 1h",
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}

	userID := e.User().ID
	guildID := *e.GuildID()
	var channelID snowflake.ID

	if vs, ok := e.Client().Caches.VoiceState(guildID, userID); ok && vs.ChannelID != nil {
		channelID = *vs.ChannelID
	}
	if channelID == 0 {
		_ = e.CreateMessage(discord.MessageCreate{
			Content: "まずボイスチャンネルに参加してからコマンドを実行してください",
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}

	// Defer the response to avoid interaction timeout and unblock the gateway
	_ = e.DeferCreateMessage(true)
	go timerManager.StartTimer(e, guildID, channelID, dur)
}

func commandStopTimer(e *events.ApplicationCommandInteractionCreate) {
	snum := len(timerManager.sessions)
	slog.Info("commandStopTimer", "sessions", snum)
	userID := e.User().ID
	guildID := *e.GuildID()
	var channelID snowflake.ID

	if vs, ok := e.Client().Caches.VoiceState(guildID, userID); ok && vs.ChannelID != nil {
		channelID = *vs.ChannelID
	}

	if channelID == 0 {
		_ = e.CreateMessage(discord.MessageCreate{
			Content: "まずボイスチャンネルに参加してからコマンドを実行してください",
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}

	_ = e.DeferCreateMessage(true)
	go timerManager.StopTimer(e, guildID, channelID)
}

func commandShowTimer(e *events.ApplicationCommandInteractionCreate) {
	snum := len(timerManager.sessions)
	slog.Info("commandShowTimer", "sessions", snum)
	userID := e.User().ID
	guildID := *e.GuildID()
	var channelID snowflake.ID

	if vs, ok := e.Client().Caches.VoiceState(guildID, userID); ok && vs.ChannelID != nil {
		channelID = *vs.ChannelID
	}

	if channelID == 0 {
		_ = e.CreateMessage(discord.MessageCreate{
			Content: "まずボイスチャンネルに参加してからコマンドを実行してください",
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}
	_ = e.DeferCreateMessage(true)
	go timerManager.ShowTimer(e, guildID, channelID)
}

type TimerManager struct {
	mu       sync.Mutex
	sessions map[snowflake.ID]*TimerSession // key: channelID
}

func (tm *TimerManager) StartTimer(e *events.ApplicationCommandInteractionCreate, guildID, channelID snowflake.ID, total time.Duration) {
	tm.mu.Lock()
	if _, ok := tm.sessions[channelID]; ok {
		tm.mu.Unlock()
		tm.updateInteractionResponse(e, "既にタイマーが作動中です")
		return
	}

	client := e.Client()

	// gather participants currently in the voice channel
	var participants []snowflake.ID
	for vs := range client.Caches.VoiceStates(guildID) {
		if vs.ChannelID != nil && *vs.ChannelID == channelID && vs.UserID != client.ID() {
			if isBot(client, guildID, vs.UserID) {
				continue
			}
			var username string
			if m, ok := getMember(client, guildID, vs.UserID); ok {
				username = m.User.Username
			} else {
				username = vs.UserID.String()
			}
			log.Printf("participant: %v", username)
			participants = append(participants, vs.UserID)
		}
	}

	if len(participants) <= 0 {
		tm.mu.Unlock()
		tm.updateInteractionResponse(e, "ボイスチャンネルに参加しているユーザーがいません")
		return
	}

	// Placeholder to prevent concurrent starts for the same channel
	tm.sessions[channelID] = nil
	tm.mu.Unlock()

	conn := client.VoiceManager.CreateConn(guildID)

	shortCtx, shortCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shortCancel()
	// Open the voice gateway. Join UNMUTED and UN-DEAFENED to provide a standard state for DAVE handshake.
	err := conn.Open(shortCtx, channelID, false, false)
	if err != nil {
		tm.updateInteractionResponse(e, "ボイスチャンネルへの接続に失敗しました: "+err.Error())
		return
	}
	slog.Info("PHASE 3: Conn opened", "channelID", channelID)

	// Check if the connection survived the handshake
	if conn.Gateway().Status() != voice.StatusReady {
		tm.updateInteractionResponse(e, "暗号化接続 (DAVE) の確立に失敗しました。時間をおいて再度お試しください")
		return
	}

	// Force the SFU to recognize us as an active participant by toggling speaking state.
	// This often resolves issues where the SFU suppresses OpCode 5 events.
	_ = conn.SetSpeaking(shortCtx, voice.SpeakingFlagMicrophone)
	time.Sleep(200 * time.Millisecond)
	_ = conn.SetSpeaking(shortCtx, voice.SpeakingFlagNone)

	// Use a separate short timeout for connection establishment
	ctx, cancel := context.WithTimeout(context.Background(), total)

	session := &TimerSession{
		cancel:           cancel,
		conn:             conn,
		Active:           true,
		Client:           client,
		GuildID:          guildID,
		ChannelID:        channelID,
		Start:            time.Now(),
		Total:            total,
		participants:     make(map[snowflake.ID]bool),
		allocated:        make(map[snowflake.ID]time.Duration),
		userSpeakingTime: make(map[snowflake.ID]time.Duration),
		muted:            make(map[snowflake.ID]bool),
		savedOverwrites:  make(map[snowflake.ID]SavedOverwrite),
		ssrcToUser:       make(map[uint32]snowflake.ID),
	}

	// Link user IDs to SSRCs when they notify to start speaking
	conn.SetEventHandlerFunc(func(gateway voice.Gateway, opCode voice.Opcode, seq int, data voice.GatewayMessageData) {
		slog.Info("voice event", "opCode", opCode, "seq", seq)
		if opCode == voice.OpcodeSpeaking {
			if speaking, ok := data.(voice.GatewayMessageDataSpeaking); ok {
				uid := speaking.UserID
				session.mu.Lock()
				if uid != 0 {
					session.ssrcToUser[speaking.SSRC] = uid
				}
				session.mu.Unlock()
			}
		}
	})

	per := total / time.Duration(len(participants))
	for _, u := range participants {
		session.participants[u] = true
		session.allocated[u] = per
		session.userSpeakingTime[u] = 0
	}

	// Start the session
	go func() {
		slog.Info("loop is started", "channel", session.ChannelID)
		defer slog.Info("loop is finished", "channel", session.ChannelID)
		for {
			select {
			case <-ctx.Done():
				session.end()
				cancel()
				return
			default:
			}

			// ReadPacket はブロッキング呼び出しのため、デッドラインを設けて
			// 定期的にループ先頭へ戻り ctx.Done() をチェックできるようにする
			deadline := time.Now().Add(200 * time.Millisecond)
			if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
				deadline = d
			}
			if err := session.conn.UDP().SetReadDeadline(deadline); err != nil {
				continue
			}

			pkt, err := session.conn.UDP().ReadPacket()
			if err != nil {
				// タイムアウト・一時エラーはループ継続
				continue
			}
			if uid, ok := session.ssrcToUser[pkt.SSRC]; ok {
				if _, ok := session.participants[uid]; !ok {
					continue
				}
				slog.Info("opus", "len", len(pkt.Opus), "seq", pkt.Sequence)
				dur := session.opusDuration(pkt.Opus)
				session.addSpeakingTime(uid, dur)
			}
		}
	}()

	tm.mu.Lock()
	tm.sessions[channelID] = session

	embed := discord.NewEmbedBuilder().
		SetTitle("タイマーを開始しました").
		SetDescriptionf("制限時間 %v (参加者 %d人)", total, len(session.participants)).
		SetColor(0x00ff00).
		SetFooterText("version: " + version)
	for uid, participating := range session.participants {
		if participating {
			var username string
			if m, ok := e.Client().Caches.Member(guildID, uid); ok {
				username = m.User.Username
			} else {
				username = uid.String()
			}
			userRemain := (session.allocated[uid] - session.userSpeakingTime[uid]).Round(time.Second)
			embed.AddField(username, fmt.Sprintf("%v", userRemain), true)
		}
	}
	tm.mu.Unlock()

	_, _ = client.Rest.CreateMessage(channelID, discord.MessageCreate{Embeds: []discord.Embed{embed.Build()}})
	tm.updateInteractionResponse(e, "タイマーを開始しました")
	slog.Info("startTimer ends")
}

func (tm *TimerManager) StopTimer(e *events.ApplicationCommandInteractionCreate, guildID, channelID snowflake.ID) {
	tm.mu.Lock()
	if session, ok := tm.sessions[channelID]; ok && session != nil {
		session.cancel()
		tm.mu.Unlock()
		tm.updateInteractionResponse(e, "タイマーを停止しました")
		return
	}
	tm.mu.Unlock()
	tm.updateInteractionResponse(e, "このチャンネルでは現在タイマーが作動していません")
	slog.Info("StopTimer ends")
}

func (tm *TimerManager) ShowTimer(e *events.ApplicationCommandInteractionCreate, guildID, channelID snowflake.ID) {
	tm.mu.Lock()
	session, ok := tm.sessions[channelID]
	tm.mu.Unlock()
	if ok && session != nil {
		eb := discord.NewEmbedBuilder().
			SetTitle("持ち時間").
			SetColor(0x0000ff).
			SetFooterText("version: " + version)

		session.mu.Lock()
		remainTotal := (session.Total - time.Since(session.Start)).Round(time.Second)
		eb.SetDescriptionf("残り時間: %v / %v", remainTotal, session.Total)

		for uid, participating := range session.participants {
			if participating {
				var username string
				if m, ok := e.Client().Caches.Member(guildID, uid); ok {
					username = m.User.Username
				} else {
					username = uid.String()
				}
				userRemain := (session.allocated[uid] - session.userSpeakingTime[uid]).Round(time.Second)
				eb.AddField(username, fmt.Sprintf("%v", userRemain), true)
			}
		}
		session.mu.Unlock()

		_, _ = e.Client().Rest.CreateMessage(channelID, discord.MessageCreate{Embeds: []discord.Embed{eb.Build()}})
		tm.updateInteractionResponse(e, "持ち時間を表示しました")
		return
	}
	tm.updateInteractionResponse(e, "このチャンネルでは現在タイマーが作動していません")
	slog.Info("ShowTimer ends")
}

func (tm *TimerManager) HandleVoiceStateUpdate(client *bot.Client, e *events.GuildVoiceStateUpdate) {
	if e.VoiceState.ChannelID == nil {
		return
	}
	if e.VoiceState.UserID == client.ID() || isBot(client, e.VoiceState.GuildID, e.VoiceState.UserID) {
		return
	}
	go func() {
		tm.mu.Lock()
		session, ok := tm.sessions[*e.VoiceState.ChannelID]
		tm.mu.Unlock()
		if !ok || session == nil {
			return
		}

		session.mu.Lock()
		defer session.mu.Unlock()

		if _, ok := session.participants[e.VoiceState.UserID]; ok {
			return
		}

		session.muteLateJoiner(e.VoiceState.UserID)
	}()
}

func (tm *TimerManager) updateInteractionResponse(e *events.ApplicationCommandInteractionCreate, content string) {
	_, _ = e.Client().Rest.UpdateInteractionResponse(e.ApplicationID(), e.Token(), discord.MessageUpdate{
		Content: &content,
	})
}

type SavedOverwrite struct {
	Exists bool
	Allow  discord.Permissions
	Deny   discord.Permissions
}

type TimerSession struct {
	mu     sync.Mutex
	cancel context.CancelFunc

	conn voice.Conn

	Active    bool
	Client    *bot.Client
	GuildID   snowflake.ID
	ChannelID snowflake.ID
	Start     time.Time
	Total     time.Duration

	participants     map[snowflake.ID]bool
	allocated        map[snowflake.ID]time.Duration
	userSpeakingTime map[snowflake.ID]time.Duration
	muted            map[snowflake.ID]bool
	savedOverwrites  map[snowflake.ID]SavedOverwrite
	ssrcToUser       map[uint32]snowflake.ID
}

// func StartSession() (*TimerSession, error) {

// }

func (session *TimerSession) samplesPerFrame(toc byte) int {
	config := toc >> 3 // 上位5bit

	switch {
	case config < 12: // SILK
		switch config & 0x3 {
		case 0:
			return 480 // 10ms
		case 1:
			return 960 // 20ms
		case 2:
			return 1920 // 40ms
		case 3:
			return 2880 // 60ms
		}
	case config < 16: // Hybrid
		switch config & 0x1 {
		case 0:
			return 1920 // 40ms
		case 1:
			return 2880 // 60ms
		}
	default: // CELT (config 16-31) ← Discordはほぼこれ
		switch config & 0x3 {
		case 0:
			return 120 // 2.5ms
		case 1:
			return 240 // 5ms
		case 2:
			return 480 // 10ms
		case 3:
			return 960 // 20ms ← Discordの実態はここ
		}
	}
	return 960 // fallback
}

// パケット1つの総サンプル数（複数フレームが入っている場合も対応）
func (session *TimerSession) totalSamples(opus []byte) int {
	if len(opus) == 0 {
		return 0
	}

	toc := opus[0]
	code := toc & 0x3 // 下位2bit = フレーム数コード
	spf := session.samplesPerFrame(toc)

	switch code {
	case 0:
		return spf // フレーム1つ
	case 1:
		return spf * 2 // フレーム2つ（等サイズ）
	case 2:
		return spf * 2 // フレーム2つ（異サイズ）
	case 3: // フレームN個（CBR/VBR）
		if len(opus) < 2 {
			return spf
		}
		numFrames := int(opus[1] & 0x3F) // 2バイト目の下位6bit
		return spf * numFrames
	}
	return spf
}

// duration を time.Duration で返す
func (session *TimerSession) opusDuration(opus []byte) time.Duration {
	samples := session.totalSamples(opus)
	// samples / 48000 秒 → Durationに変換
	return time.Duration(samples) * time.Second / sampleRate
}

func (session *TimerSession) addSpeakingTime(uid snowflake.ID, dur time.Duration) {
	session.mu.Lock()
	defer session.mu.Unlock()

	if b, ok := session.participants[uid]; !ok || !b {
		session.muteLateJoiner(uid)
		return
	}
	if _, ok := session.allocated[uid]; !ok {
		return
	}
	if b, ok := session.muted[uid]; ok && b {
		return
	}
	if d, ok := session.userSpeakingTime[uid]; ok {
		session.userSpeakingTime[uid] = d + dur
	} else {
		session.userSpeakingTime[uid] = dur
	}
	alloc := session.allocated[uid]
	used := session.userSpeakingTime[uid]
	if used >= alloc {
		session.muteUser(uid, "時間超過")
		return
	}
}

func (session *TimerSession) muteLateJoiner(uid snowflake.ID) {
	session.participants[uid] = true
	session.allocated[uid] = 0
	session.muteUser(uid, "途中参加")
}

func (session *TimerSession) muteUser(uid snowflake.ID, reason string) {
	session.mu.Lock()
	if !session.Active {
		session.mu.Unlock()
		return
	}
	if b, ok := session.muted[uid]; ok && b {
		session.mu.Unlock()
		return
	}
	session.muted[uid] = true
	session.mu.Unlock()

	// Offload REST calls to a goroutine to avoid blocking gateway / voice threads
	go func() {
		ch, err := session.Client.Rest.GetChannel(session.ChannelID)
		if err != nil {
			slog.Error("Failed to get channel for mute", "error", err, "channelID", session.ChannelID)
			return
		}

		var allow discord.Permissions
		var deny discord.Permissions
		found := false
		if tc, ok := ch.(discord.GuildChannel); ok {
			for _, po := range tc.PermissionOverwrites() {
				if mpo, ok := po.(discord.MemberPermissionOverwrite); ok && mpo.ID() == uid {
					allow = mpo.Allow
					deny = mpo.Deny
					found = true
					break
				}
			}
		}

		session.mu.Lock()
		if found {
			session.savedOverwrites[uid] = SavedOverwrite{Exists: true, Allow: allow, Deny: deny}
		} else {
			session.savedOverwrites[uid] = SavedOverwrite{Exists: false}
		}
		session.mu.Unlock()

		denySpeak := discord.PermissionSpeak | deny
		allowSpeak := allow &^ discord.PermissionSpeak

		_ = session.Client.Rest.UpdatePermissionOverwrite(session.ChannelID, uid, discord.MemberPermissionOverwriteUpdate{
			Allow: &allowSpeak,
			Deny:  &denySpeak,
		})

		embed := discord.NewEmbedBuilder().
			SetTitle(fmt.Sprintf("%s", reason)).
			SetDescriptionf("<@%s> さんをミュートしました (%s)", uid, reason).
			SetColor(0xffff00).
			SetFooterText("version: " + version).
			Build()
		_, _ = session.Client.Rest.CreateMessage(session.ChannelID, discord.MessageCreate{Embeds: []discord.Embed{embed}})
	}()
}

func (session *TimerSession) end() {
	slog.Info("session ends", "channel", session.ChannelID)
	session.mu.Lock()
	if !session.Active {
		session.mu.Unlock()
		return
	}
	session.Active = false
	participants := make([]snowflake.ID, 0, len(session.participants))
	for uid := range session.participants {
		participants = append(participants, uid)
	}
	session.mu.Unlock()

	slog.Info("timer exiting, closing connection", "channel_id", session.ChannelID)
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shortCancel()
	go session.conn.Close(shortCtx)

	embed := discord.NewEmbedBuilder().
		SetTitle("タイマーが終了しました").
		SetDescription("ミュート設定を解除します").
		SetColor(0xff0000).
		SetFooterText("version: " + version).
		Build()

	_, _ = session.Client.Rest.CreateMessage(session.ChannelID, discord.MessageCreate{Embeds: []discord.Embed{embed}})

	for _, uid := range participants {
		session.mu.Lock()
		if session.muted[uid] {
			if so, ok := session.savedOverwrites[uid]; ok {
				if so.Exists {
					_ = session.Client.Rest.UpdatePermissionOverwrite(session.ChannelID, uid, discord.MemberPermissionOverwriteUpdate{
						Allow: &so.Allow,
						Deny:  &so.Deny,
					})
				} else {
					_ = session.Client.Rest.DeletePermissionOverwrite(session.ChannelID, uid)
				}
				delete(session.savedOverwrites, uid)
			} else {
				_ = session.Client.Rest.DeletePermissionOverwrite(session.ChannelID, uid)
			}
		}
		session.mu.Unlock()
	}

	timerManager.mu.Lock()
	delete(timerManager.sessions, session.ChannelID)
	timerManager.mu.Unlock()
	slog.Info("timer session completely removed", "channel_id", session.ChannelID)
}

func getMember(client *bot.Client, guildID snowflake.ID, userID snowflake.ID) (discord.Member, bool) {
	if m, ok := client.Caches.Member(guildID, userID); ok {
		return m, true
	}
	// キャッシュにない場合はREST APIにフォールバック
	m, err := client.Rest.GetMember(guildID, userID)
	if err != nil || m == nil {
		return discord.Member{}, false
	}
	return *m, true
}

func isBot(client *bot.Client, guildID snowflake.ID, userID snowflake.ID) bool {
	if m, ok := getMember(client, guildID, userID); ok {
		return m.User.Bot
	}
	// メンバー情報が取得できない場合はBOTとみなして除外する
	return true
}
