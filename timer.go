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

type SavedOverwrite struct {
	Exists bool
	Allow  discord.Permissions
	Deny   discord.Permissions
}

type TimerSession struct {
	Client    *bot.Client
	GuildID   snowflake.ID
	ChannelID snowflake.ID
	Total     time.Duration
	Start     time.Time

	userSpeakingTime map[snowflake.ID]time.Duration
	lastStart        map[snowflake.ID]time.Time
	allocated        map[snowflake.ID]time.Duration
	participants     map[snowflake.ID]bool
	muted            map[snowflake.ID]bool
	savedOverwrites  map[snowflake.ID]SavedOverwrite
	timers           map[snowflake.ID]*time.Timer

	mu sync.Mutex
	// ctx    context.Context
	cancel context.CancelFunc
	Active bool
}

type TimerManager struct {
	mu       sync.Mutex
	sessions map[snowflake.ID]*TimerSession // key: channelID
}

var timerManager = &TimerManager{sessions: make(map[snowflake.ID]*TimerSession)}

func (tm *TimerManager) StartTimer(e *events.ApplicationCommandInteractionCreate, guildID, channelID snowflake.ID, total time.Duration) {
	client := e.Client()

	tm.mu.Lock()
	if _, ok := tm.sessions[channelID]; ok {
		tm.mu.Unlock()
		updateInteractionResponse(e, "このチャンネルでは既にタイマーが動作中です")
		return
	}

	// gather participants currently in the voice channel
	var participants []snowflake.ID
	for vs := range client.Caches.VoiceStates(guildID) {
		if vs.ChannelID != nil && *vs.ChannelID == channelID && vs.UserID != client.ID() {
			if isBot(client, guildID, vs.UserID) {
				continue
			}
			log.Printf("participant: %v", vs.UserID)
			participants = append(participants, vs.UserID)
		}
	}

	if len(participants) == 0 {
		tm.mu.Unlock()
		updateInteractionResponse(e, "ボイスチャンネルに参加しているユーザーがいません")
		return
	}

	// Placeholder to prevent concurrent starts for the same channel
	tm.sessions[channelID] = nil
	tm.mu.Unlock()

	// Run voice connection and initialization in a separate goroutine to avoid gateway deadlock
	// go func() {
	slog.Info("PHASE 1: StartTimer (async) called", "guildID", guildID, "channelID", channelID, "total", total)

	// Use a separate short timeout for connection establishment
	ctx, cancel := context.WithTimeout(context.Background(), total)

	conn := client.VoiceManager.CreateConn(guildID)

	slog.Info("PHASE 2: Conn opening...", "guildID", guildID)
	// Open the voice gateway. Join UNMUTED and UN-DEAFENED to provide a standard state for DAVE handshake.
	err := conn.Open(ctx, channelID, false, false)
	if err != nil {
		slog.Error("PHASE 2.5: Conn open failed", "error", err)
		updateInteractionResponse(e, "ボイスチャンネルへの接続に失敗しました: "+err.Error())
		cancel()
		return
	}
	slog.Info("PHASE 3: Conn opened", "channelID", channelID)

	// Check if the connection survived the handshake
	if conn.Gateway().Status() != voice.StatusReady {
		slog.Error("PHASE 3.5: Conn dropped during DAVE handshake", "status", conn.Gateway().Status())
		updateInteractionResponse(e, "暗号化接続 (DAVE) の確立に失敗しました。時間をおいて再度お試しください。")
		cancel()
		return
	}

	session := &TimerSession{
		Client:           client,
		GuildID:          guildID,
		ChannelID:        channelID,
		Total:            total,
		Start:            time.Now(),
		userSpeakingTime: make(map[snowflake.ID]time.Duration),
		lastStart:        make(map[snowflake.ID]time.Time),
		allocated:        make(map[snowflake.ID]time.Duration),
		participants:     make(map[snowflake.ID]bool),
		muted:            make(map[snowflake.ID]bool),
		savedOverwrites:  make(map[snowflake.ID]SavedOverwrite),
		timers:           make(map[snowflake.ID]*time.Timer),
		Active:           true,
		cancel:           cancel,
	}

	// Now that it's stable, set the event handler for participant tracking
	conn.SetEventHandlerFunc(func(gateway voice.Gateway, opCode voice.Opcode, seq int, data voice.GatewayMessageData) {
		if opCode == voice.OpcodeSpeaking {
			if speaking, ok := data.(voice.GatewayMessageDataSpeaking); ok {
				if isBot(client, guildID, speaking.UserID) {
					return
				}
				var username string
				if m, ok := client.Caches.Member(guildID, speaking.UserID); ok {
					username = m.User.Username
				} else {
					username = speaking.UserID.String()
				}
				slog.Info("speaking", "speaking", speaking.Speaking, "username", username)
				if (speaking.Speaking & voice.SpeakingFlagMicrophone) != 0 {
					timerManager.handleSpeakingStart(session, speaking.UserID)
				} else {
					timerManager.handleSpeakingStop(session, speaking.UserID)
				}
			}
		}
	})

	per := total / time.Duration(len(participants))
	for _, u := range participants {
		session.participants[u] = true
		session.allocated[u] = per
	}

	go session.run(ctx)

	tm.mu.Lock()
	tm.sessions[channelID] = session
	tm.mu.Unlock()

	embed := discord.NewEmbedBuilder().
		SetTitle("タイマーを開始しました").
		SetDescriptionf("合計 %v、参加者 %d、各自割当 %v", total, len(participants), per).
		SetColor(0x00ff00).
		SetFooterText("version: " + version).
		Build()

	updateInteractionResponse(e, "タイマーを開始しました")
	_, _ = client.Rest.CreateMessage(channelID, discord.MessageCreate{Embeds: []discord.Embed{embed}})
	// }()
}

func (tm *TimerManager) StopTimer(client *bot.Client, guildID, channelID snowflake.ID) error {
	tm.mu.Lock()
	if session, ok := tm.sessions[channelID]; ok && session != nil {
		tm.mu.Unlock()
		session.cancel()
		return nil
	}
	tm.mu.Unlock()
	return fmt.Errorf("このチャンネルでは現在タイマーが作動していません")
}

func (tm *TimerManager) ListTimer(client *bot.Client, guildID, channelID snowflake.ID) error {
	tm.mu.Lock()
	session, ok := tm.sessions[channelID]
	tm.mu.Unlock()
	if ok && session != nil {
		eb := discord.NewEmbedBuilder().
			SetTitle("残りの持ち時間").
			SetColor(0x00ff00).
			SetFooterText("version: " + version)

		session.mu.Lock()
		remainTotal := (session.Total - time.Since(session.Start)).Round(time.Second)
		eb.SetDescriptionf("全体の残り時間: %v", remainTotal)

		for uid, participating := range session.participants {
			if participating {
				var username string
				if m, ok := client.Caches.Member(guildID, uid); ok {
					username = m.User.Username
				} else {
					username = uid.String()
				}
				userRemain := (session.allocated[uid] - session.userSpeakingTime[uid]).Round(time.Second)
				eb.AddField(username, fmt.Sprintf("%v", userRemain), true)
			}
		}
		session.mu.Unlock()

		_, _ = client.Rest.CreateMessage(channelID, discord.MessageCreate{Embeds: []discord.Embed{eb.Build()}})
		return nil
	}
	return fmt.Errorf("このチャンネルでは現在タイマーが作動していません")
}

func (ts *TimerSession) run(ctx context.Context) {
	slog.Info("timer starts", "channel_id", ts.ChannelID, "total", ts.Total)
	<-ctx.Done()
	slog.Info("timer finished or canceled", "channel_id", ts.ChannelID)
	ts.cancel()
	ts.end()
}

func (ts *TimerSession) end() {
	ts.mu.Lock()
	if !ts.Active {
		ts.mu.Unlock()
		return
	}
	ts.Active = false
	participants := make([]snowflake.ID, 0, len(ts.participants))
	for uid := range ts.participants {
		participants = append(participants, uid)
	}
	ts.mu.Unlock()

	slog.Info("timer exiting, closing connection", "channel_id", ts.ChannelID)
	shortCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go ts.Client.VoiceManager.Close(shortCtx)

	embed := discord.NewEmbedBuilder().
		SetTitle("全体の持ち時間が終了しました").
		SetDescription("ミュート設定を解除します").
		SetColor(0xff0000).
		SetFooterText("version: " + version).
		Build()

	_, _ = ts.Client.Rest.CreateMessage(ts.ChannelID, discord.MessageCreate{Embeds: []discord.Embed{embed}})

	for _, uid := range participants {
		ts.mu.Lock()
		if ts.muted[uid] {
			if so, ok := ts.savedOverwrites[uid]; ok {
				if so.Exists {
					_ = ts.Client.Rest.UpdatePermissionOverwrite(ts.ChannelID, uid, discord.MemberPermissionOverwriteUpdate{
						Allow: &so.Allow,
						Deny:  &so.Deny,
					})
				} else {
					_ = ts.Client.Rest.DeletePermissionOverwrite(ts.ChannelID, uid)
				}
				delete(ts.savedOverwrites, uid)
			} else {
				_ = ts.Client.Rest.DeletePermissionOverwrite(ts.ChannelID, uid)
			}
		}
		ts.mu.Unlock()
	}

	timerManager.mu.Lock()
	delete(timerManager.sessions, ts.ChannelID)
	timerManager.mu.Unlock()
	slog.Info("timer session completely removed", "channel_id", ts.ChannelID)
}

func (tm *TimerManager) handleSpeakingStart(session *TimerSession, uid snowflake.ID) {
	session.mu.Lock()
	defer session.mu.Unlock()

	if isBot(session.Client, session.GuildID, uid) {
		return
	}

	if !session.participants[uid] {
		tm.muteLateJoiner(session, uid)
		return
	}

	if session.muted[uid] {
		return
	}

	session.lastStart[uid] = time.Now()
	if t, ok := session.timers[uid]; ok {
		t.Stop()
		delete(session.timers, uid)
	}

	alloc := session.allocated[uid]
	used := session.userSpeakingTime[uid]
	if alloc > 0 {
		remaining := alloc - used
		if remaining <= 0 {
			tm.muteUserNoLock(session, uid, "時間超過")
			return
		}

		session.timers[uid] = time.AfterFunc(remaining, func() {
			tm.muteUserFromTimer(session, uid)
		})
	}
}

func (tm *TimerManager) handleSpeakingStop(session *TimerSession, uid snowflake.ID) {
	session.mu.Lock()
	defer session.mu.Unlock()

	if start, ok := session.lastStart[uid]; ok && !start.IsZero() {
		dur := time.Since(start)
		session.userSpeakingTime[uid] += dur
		session.lastStart[uid] = time.Time{}

		if t, ok := session.timers[uid]; ok {
			t.Stop()
			delete(session.timers, uid)
		}

		if session.userSpeakingTime[uid] >= session.allocated[uid] && session.allocated[uid] > 0 {
			tm.muteUserNoLock(session, uid, "時間超過")
		}
	}
}

func (tm *TimerManager) muteLateJoiner(session *TimerSession, uid snowflake.ID) {
	session.participants[uid] = true
	session.allocated[uid] = 0
	tm.muteUserNoLock(session, uid, "途中参加")
}

func (tm *TimerManager) muteUserNoLock(session *TimerSession, uid snowflake.ID, reason string) {
	if !session.Active {
		return
	}
	if session.muted[uid] {
		return
	}
	session.muted[uid] = true

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
			SetTitle(fmt.Sprintf("%s: <@%s> さん", reason, uid)).
			SetDescriptionf("<@%s> さんをミュートしました (%s)", uid, reason).
			SetColor(0xffff00).
			SetFooterText("version: " + version).
			Build()
		_, _ = session.Client.Rest.CreateMessage(session.ChannelID, discord.MessageCreate{Embeds: []discord.Embed{embed}})
	}()
}

func (tm *TimerManager) muteUserFromTimer(session *TimerSession, uid snowflake.ID) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if !session.Active {
		return
	}
	if start, ok := session.lastStart[uid]; ok && !start.IsZero() {
		tm.muteUserNoLock(session, uid, "時間超過")
	}
}

func (tm *TimerManager) HandleVoiceStateUpdate(client *bot.Client, e *events.GuildVoiceStateUpdate) {
	if e.VoiceState.UserID == client.ID() || isBot(client, e.VoiceState.GuildID, e.VoiceState.UserID) {
		return
	}

	if e.VoiceState.ChannelID == nil {
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

		if session.participants[e.VoiceState.UserID] {
			return
		}

		tm.muteLateJoiner(session, e.VoiceState.UserID)
	}()
}

func handleTimerCommand(e *events.ApplicationCommandInteractionCreate) {
	data := e.SlashCommandInteractionData()
	input, _ := data.OptString(optionNameDuration)
	dur, err := time.ParseDuration(input)
	if err != nil {
		_ = e.CreateMessage(discord.MessageCreate{
			Content: "有効な時間を指定してください。例: 30m, 1h",
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
			Content: "まずボイスチャンネルに参加してからコマンドを実行してください。",
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}

	// Defer the response to avoid interaction timeout and unblock the gateway
	_ = e.DeferCreateMessage(true)
	go timerManager.StartTimer(e, guildID, channelID, dur)
}

func handleStopTimerCommand(e *events.ApplicationCommandInteractionCreate) {
	userID := e.User().ID
	guildID := *e.GuildID()
	var channelID snowflake.ID

	if vs, ok := e.Client().Caches.VoiceState(guildID, userID); ok && vs.ChannelID != nil {
		channelID = *vs.ChannelID
	}

	if channelID == 0 {
		_ = e.CreateMessage(discord.MessageCreate{
			Content: "まずボイスチャンネルに参加してからコマンドを実行してください。",
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}

	if err := timerManager.StopTimer(e.Client(), guildID, channelID); err != nil {
		_ = e.CreateMessage(discord.MessageCreate{
			Content: "タイマーの停止に失敗しました: " + err.Error(),
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}
	_ = e.CreateMessage(discord.MessageCreate{
		Content: "タイマーを停止しました",
		Flags:   discord.MessageFlagEphemeral,
	})
}

func handleListTimerCommand(e *events.ApplicationCommandInteractionCreate) {
	userID := e.User().ID
	guildID := *e.GuildID()
	var channelID snowflake.ID

	if vs, ok := e.Client().Caches.VoiceState(guildID, userID); ok && vs.ChannelID != nil {
		channelID = *vs.ChannelID
	}

	if channelID == 0 {
		_ = e.CreateMessage(discord.MessageCreate{
			Content: "まずボイスチャンネルに参加してからコマンドを実行してください。",
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}

	if err := timerManager.ListTimer(e.Client(), guildID, channelID); err != nil {
		_ = e.CreateMessage(discord.MessageCreate{
			Content: "持ち時間の表示に失敗しました: " + err.Error(),
			Flags:   discord.MessageFlagEphemeral,
		})
		return
	}
	_ = e.CreateMessage(discord.MessageCreate{
		Content: "持ち時間を表示しました",
		Flags:   discord.MessageFlagEphemeral,
	})
}

func updateInteractionResponse(e *events.ApplicationCommandInteractionCreate, content string) {
	_, _ = e.Client().Rest.UpdateInteractionResponse(e.ApplicationID(), e.Token(), discord.MessageUpdate{
		Content: &content,
	})
}

func isBot(client *bot.Client, guildID snowflake.ID, userID snowflake.ID) bool {
	if m, ok := client.Caches.Member(guildID, userID); ok {
		return m.User.Bot
	}
	return false
}
