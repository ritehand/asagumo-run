package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type SavedOverwrite struct {
	Exists bool
	Allow  int64
	Deny   int64
}

type TimerSession struct {
	Session    *discordgo.Session
	Connection *discordgo.VoiceConnection
	GuildID    string
	ChannelID  string
	Total      time.Duration
	Start      time.Time

	userSpeakingTime map[string]time.Duration
	lastStart        map[string]time.Time
	allocated        map[string]time.Duration
	participants     map[string]bool
	muted            map[string]bool
	savedOverwrites  map[string]SavedOverwrite
	timers           map[string]*time.Timer

	mu sync.Mutex
	// ctx    context.Context
	cancel context.CancelFunc
	Active bool
}

type TimerManager struct {
	mu       sync.Mutex
	sessions map[string]*TimerSession // key: channelID
}

var timerManager = &TimerManager{sessions: make(map[string]*TimerSession)}

func (tm *TimerManager) StartTimer(s *discordgo.Session, guildID, channelID, replyChannelID string, total time.Duration) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if _, ok := tm.sessions[channelID]; ok {
		return fmt.Errorf("このチャンネルでは既にタイマーが動作中です")
	}

	// gather participants currently in the voice channel
	var participants []string
	guild, _ := s.State.Guild(guildID)
	if guild != nil {
		for _, vs := range guild.VoiceStates {
			if vs.ChannelID == channelID && vs.UserID != s.State.User.ID {
				if isBot(s, guildID, vs.UserID) {
					continue
				}
				participants = append(participants, vs.UserID)
			}
		}
	}

	if len(participants) == 0 {
		return fmt.Errorf("ボイスチャンネルに参加しているユーザーがいません")
	}

	ctx, cancel := context.WithTimeout(context.Background(), total)

	vc, err := s.ChannelVoiceJoin(ctx, guildID, channelID, true, false)
	if err != nil {
		cancel()
		return err
	}
	vc.AddHandler(func(_ *discordgo.VoiceConnection, vs *discordgo.VoiceSpeakingUpdate) {
		timerManager.HandleSpeakingUpdate(s, vs)
	})

	session := &TimerSession{
		Session:          s,
		Connection:       vc,
		GuildID:          guildID,
		ChannelID:        channelID,
		Total:            total,
		Start:            time.Now(),
		userSpeakingTime: make(map[string]time.Duration),
		lastStart:        make(map[string]time.Time),
		allocated:        make(map[string]time.Duration),
		participants:     make(map[string]bool),
		muted:            make(map[string]bool),
		savedOverwrites:  make(map[string]SavedOverwrite),
		timers:           make(map[string]*time.Timer),
		Active:           true,
		// ctx:              ctx,
		cancel: cancel,
	}

	per := total / time.Duration(len(participants))
	for _, u := range participants {
		session.participants[u] = true
		session.allocated[u] = per
	}

	go session.start(ctx)

	tm.sessions[channelID] = session

	// s.ChannelMessageSend(replyChannelID, fmt.Sprintf("タイマーを開始しました。合計 %v、参加者 %d、各自割当 %v", total, len(participants), per))
	embed := &discordgo.MessageEmbed{
		Title:       "タイマーを開始しました",
		Description: fmt.Sprintf("合計 %v、参加者 %d、各自割当 %v", total, len(participants), per),
		Color:       0x00ff00,
		// Fields: []*discordgo.MessageEmbedField{
		// 	{
		// 		Name:   "項目1",
		// 		Value:  "内容1",
		// 		Inline: true,
		// 	},
		// 	{
		// 		Name:   "項目2",
		// 		Value:  "内容2",
		// 		Inline: true,
		// 	},
		// },
		Footer: &discordgo.MessageEmbedFooter{
			Text: fmt.Sprintf("version: %s", version),
		},
	}
	_, err = s.ChannelMessageSendEmbed(channelID, embed)
	if err != nil {
		fmt.Println("Embedの送信に失敗しました:", err)
	}

	return nil
}

func (tm *TimerManager) StopTimer(s *discordgo.Session, guildID, channelID, replyChannelID string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if session, ok := tm.sessions[channelID]; ok {
		session.cancel()
		return nil
	} else {
		return fmt.Errorf("このチャンネルでは現在タイマーが作動していません")
	}
}

func (ts *TimerSession) start(ctx context.Context) {
	log.Printf("timer starts %s\n", ts.ChannelID)
	<-ctx.Done()
	log.Printf("timer canceled: %s\n", ts.ChannelID)
	ts.exit()
	ts.cancel()
}

// exit ends the timer session: announces, restores stored overwrites (or deletes), and removes session.
func (ts *TimerSession) exit() {
	ts.mu.Lock()
	if !ts.Active {
		ts.mu.Unlock()
		return
	}
	ts.Active = false
	participants := make([]string, 0, len(ts.participants))
	for uid := range ts.participants {
		participants = append(participants, uid)
	}
	ts.mu.Unlock()

	// ts.Session.ChannelMessageSend(ts.ChannelID, "全体の持ち時間が終了しました。ミュート設定を解除します。")
	embed := &discordgo.MessageEmbed{
		Title:       "全体の持ち時間が終了しました",
		Description: "ミュート設定を解除します",
		Color:       0xff0000,
		// Fields: []*discordgo.MessageEmbedField{
		// 	{
		// 		Name:   "項目1",
		// 		Value:  "内容1",
		// 		Inline: true,
		// 	},
		// 	{
		// 		Name:   "項目2",
		// 		Value:  "内容2",
		// 		Inline: true,
		// 	},
		// },
		Footer: &discordgo.MessageEmbedFooter{
			Text: fmt.Sprintf("version: %s", version),
		},
	}
	_, err := ts.Session.ChannelMessageSendEmbed(ts.ChannelID, embed)
	if err != nil {
		fmt.Println("Embedの送信に失敗しました:", err)
	}
	// restore per-channel permission overwrites we recorded (or delete if none existed)
	for _, uid := range participants {
		if ts.muted[uid] {
			if so, ok := ts.savedOverwrites[uid]; ok {
				if so.Exists {
					if err := ts.Session.ChannelPermissionSet(ts.ChannelID, uid, discordgo.PermissionOverwriteTypeMember, so.Allow, so.Deny); err != nil {
						log.Println("ChannelPermissionSet(restore) failed:", err)
						notifyAdmin(ts.Session, ts.GuildID, fmt.Sprintf("チャンネル復元に失敗しました: channel=%s user=%s err=%v", ts.ChannelID, uid, err))
					}
				} else {
					if err := ts.Session.ChannelPermissionDelete(ts.ChannelID, uid); err != nil {
						log.Println("ChannelPermissionDelete(unmute) failed:", err)
						notifyAdmin(ts.Session, ts.GuildID, fmt.Sprintf("チャンネル復元(削除)に失敗しました: channel=%s user=%s err=%v", ts.ChannelID, uid, err))
					}
				}
				delete(ts.savedOverwrites, uid)
			} else {
				// fallback: try delete
				if err := ts.Session.ChannelPermissionDelete(ts.ChannelID, uid); err != nil {
					log.Println("ChannelPermissionDelete(unmute) failed:", err)
					notifyAdmin(ts.Session, ts.GuildID, fmt.Sprintf("チャンネル復元(削除-fallback)に失敗しました: channel=%s user=%s err=%v", ts.ChannelID, uid, err))
				}
			}
		}
	}
	if err := ts.Connection.Disconnect(context.Background()); err != nil {
		log.Println("Connection.Disconnect failed:", err)
		notifyAdmin(ts.Session, ts.GuildID, fmt.Sprintf("Connection.Disconnectに失敗しました: channel=%s err=%v", ts.ChannelID, err))

	}

	// remove session
	timerManager.mu.Lock()
	delete(timerManager.sessions, ts.ChannelID)
	timerManager.mu.Unlock()
	log.Printf("timer exits: %s\n", ts.ChannelID)
}

// HandleSpeakingUpdate processes VoiceSpeakingUpdate events for active timers
func (tm *TimerManager) HandleSpeakingUpdate(s *discordgo.Session, v *discordgo.VoiceSpeakingUpdate) {
	// find session by channel id
	uid := v.UserID

	// copy sessions to avoid holding tm.mu while processing
	tm.mu.Lock()
	sessions := make([]*TimerSession, 0, len(tm.sessions))
	for _, ss := range tm.sessions {
		sessions = append(sessions, ss)
	}
	tm.mu.Unlock()

	for _, session := range sessions {
		session.mu.Lock()
		// skip bot users (per-session guild)
		if isBot(s, session.GuildID, uid) {
			session.mu.Unlock()
			continue
		}
		// process only if the user is tracked in this session
		// If user is not part of participants set, they may have joined mid-session.
		if !session.participants[uid] {
			// enforce per-channel mute for late joiners
			session.participants[uid] = true
			session.allocated[uid] = 0
			// record existing overwrite then deny SPEAK permission in this channel (bit 1<<21)
			if _, ok := session.savedOverwrites[uid]; !ok {
				// try to capture existing overwrite
				ch, err := s.Channel(session.ChannelID)
				if err == nil {
					found := false
					for _, po := range ch.PermissionOverwrites {
						if po.ID == uid && po.Type == discordgo.PermissionOverwriteTypeMember {
							session.savedOverwrites[uid] = SavedOverwrite{Exists: true, Allow: po.Allow, Deny: po.Deny}
							found = true
							break
						}
					}
					if !found {
						session.savedOverwrites[uid] = SavedOverwrite{Exists: false}
					}
				} else {
					// on error, mark as not existed (best-effort)
					session.savedOverwrites[uid] = SavedOverwrite{Exists: false}
				}
			}
			denySpeak := int64(1 << 21)
			if err := s.ChannelPermissionSet(session.ChannelID, uid, discordgo.PermissionOverwriteTypeMember, 0, denySpeak); err != nil {
				log.Println("ChannelPermissionSet(mute late join) failed:", err)
			} else {
				session.muted[uid] = true
				// s.ChannelMessageSend(session.ChannelID, fmt.Sprintf("<@%s> さんは途中参加のためミュートしました。", uid))
				embed := &discordgo.MessageEmbed{
					Title:       fmt.Sprintf("途中参加: <@%s> さん", uid),
					Description: fmt.Sprintf("<@%s> さんは途中参加のためミュートしました", uid),
					Color:       0xffff00,
					// Fields: []*discordgo.MessageEmbedField{
					// 	{
					// 		Name:   "項目1",
					// 		Value:  "内容1",
					// 		Inline: true,
					// 	},
					// 	{
					// 		Name:   "項目2",
					// 		Value:  "内容2",
					// 		Inline: true,
					// 	},
					// },
					Footer: &discordgo.MessageEmbedFooter{
						Text: fmt.Sprintf("version: %s", version),
					},
				}
				_, err = s.ChannelMessageSendEmbed(session.ChannelID, embed)
				if err != nil {
					fmt.Println("Embedの送信に失敗しました:", err)
				}
			}
			session.mu.Unlock()
			continue
		}

		if v.Speaking {
			// start speaking: record start time and schedule immediate-stop timer
			if session.muted[uid] {
				// already muted; ignore
				session.mu.Unlock()
				continue
			}
			session.lastStart[uid] = time.Now()
			// cancel any previous timer
			if t, ok := session.timers[uid]; ok {
				t.Stop()
				delete(session.timers, uid)
			}

			alloc := session.allocated[uid]
			used := session.userSpeakingTime[uid]
			// if user has no allocation or already exhausted, mute immediately
			if alloc > 0 {
				remaining := alloc - used
				if remaining <= 0 {
					// immediate mute — save existing overwrite first if needed
					if _, ok := session.savedOverwrites[uid]; !ok {
						ch, err := s.Channel(session.ChannelID)
						if err == nil {
							found := false
							for _, po := range ch.PermissionOverwrites {
								if po.ID == uid && po.Type == discordgo.PermissionOverwriteTypeMember {
									session.savedOverwrites[uid] = SavedOverwrite{Exists: true, Allow: po.Allow, Deny: po.Deny}
									found = true
									break
								}
							}
							if !found {
								session.savedOverwrites[uid] = SavedOverwrite{Exists: false}
							}
						} else {
							session.savedOverwrites[uid] = SavedOverwrite{Exists: false}
						}
					}
					denySpeak := int64(1 << 21)
					if err := s.ChannelPermissionSet(session.ChannelID, uid, discordgo.PermissionOverwriteTypeMember, 0, denySpeak); err == nil {
						session.muted[uid] = true
						// s.ChannelMessageSend(session.ChannelID, fmt.Sprintf("<@%s> さんが割当時間を超えたためミュートしました。", uid))
						embed := &discordgo.MessageEmbed{
							Title:       fmt.Sprintf("時間超過: <@%s> さん", uid),
							Description: fmt.Sprintf("<@%s> さんが割当時間を超えたためミュートしました。", uid),
							Color:       0xffff00,
							// Fields: []*discordgo.MessageEmbedField{
							// 	{
							// 		Name:   "項目1",
							// 		Value:  "内容1",
							// 		Inline: true,
							// 	},
							// 	{
							// 		Name:   "項目2",
							// 		Value:  "内容2",
							// 		Inline: true,
							// 	},
							// },
							Footer: &discordgo.MessageEmbedFooter{
								Text: fmt.Sprintf("version: %s", version),
							},
						}
						_, err = s.ChannelMessageSendEmbed(session.ChannelID, embed)
						if err != nil {
							fmt.Println("Embedの送信に失敗しました:", err)
						}
					} else {
						log.Println("ChannelPermissionSet(immediate mute) failed:", err)
					}
					session.mu.Unlock()
					continue
				}

				// schedule a timer to fire when remaining elapses
				uidCopy := uid
				chID := session.ChannelID
				t := time.AfterFunc(remaining, func() {
					// lock session to check speaking state and mute flag
					session.mu.Lock()
					// ensure user is still speaking
					if start, ok := session.lastStart[uidCopy]; !ok || start.IsZero() {
						session.mu.Unlock()
						return
					}
					if session.muted[uidCopy] {
						session.mu.Unlock()
						return
					}
					// prepare to mute: ensure we recorded existing overwrite
					if _, ok := session.savedOverwrites[uidCopy]; !ok {
						// unlock while calling API
						session.mu.Unlock()
						ch, err := s.Channel(chID)
						if err == nil {
							found := false
							for _, po := range ch.PermissionOverwrites {
								if po.ID == uidCopy && po.Type == discordgo.PermissionOverwriteTypeMember {
									session.mu.Lock()
									session.savedOverwrites[uidCopy] = SavedOverwrite{Exists: true, Allow: po.Allow, Deny: po.Deny}
									session.mu.Unlock()
									found = true
									break
								}
							}
							if !found {
								session.mu.Lock()
								session.savedOverwrites[uidCopy] = SavedOverwrite{Exists: false}
								session.mu.Unlock()
							}
						} else {
							session.mu.Lock()
							session.savedOverwrites[uidCopy] = SavedOverwrite{Exists: false}
							session.mu.Unlock()
						}
						session.mu.Lock()
					}
					session.muted[uidCopy] = true
					session.mu.Unlock()

					// apply channel permission mute
					denySpeak := int64(1 << 21)
					if err := s.ChannelPermissionSet(chID, uidCopy, discordgo.PermissionOverwriteTypeMember, 0, denySpeak); err == nil {
						// s.ChannelMessageSend(chID, fmt.Sprintf("<@%s> さんが割当時間を超えたためミュートしました。", uidCopy))
						embed := &discordgo.MessageEmbed{
							Title:       fmt.Sprintf("時間超過: <@%s> さん", uid),
							Description: fmt.Sprintf("<@%s> さんが割当時間を超えたためミュートしました", uid),
							Color:       0xffff00,
							// Fields: []*discordgo.MessageEmbedField{
							// 	{
							// 		Name:   "項目1",
							// 		Value:  "内容1",
							// 		Inline: true,
							// 	},
							// 	{
							// 		Name:   "項目2",
							// 		Value:  "内容2",
							// 		Inline: true,
							// 	},
							// },
							Footer: &discordgo.MessageEmbedFooter{
								Text: fmt.Sprintf("version: %s", version),
							},
						}
						_, err = s.ChannelMessageSendEmbed(session.ChannelID, embed)
						if err != nil {
							fmt.Println("Embedの送信に失敗しました:", err)
						}
					} else {
						log.Println("ChannelPermissionSet(mute timer) failed:", err)
					}
				})
				session.timers[uid] = t
			}

			session.mu.Unlock()
			continue
		}

		// speaking stopped: accumulate time and cancel any running timer
		if start, ok := session.lastStart[uid]; ok && !start.IsZero() {
			dur := time.Since(start)
			session.userSpeakingTime[uid] += dur
			session.lastStart[uid] = time.Time{}

			// cancel pending timer if exists
			if t, ok := session.timers[uid]; ok {
				t.Stop()
				delete(session.timers, uid)
			}

			alloc := session.allocated[uid]
			if alloc > 0 && session.userSpeakingTime[uid] > alloc {
				// set per-channel permission overwrite to deny speaking (save overwrite first)
				if _, ok := session.savedOverwrites[uid]; !ok {
					ch, err := s.Channel(session.ChannelID)
					if err == nil {
						found := false
						for _, po := range ch.PermissionOverwrites {
							if po.ID == uid && po.Type == discordgo.PermissionOverwriteTypeMember {
								session.savedOverwrites[uid] = SavedOverwrite{Exists: true, Allow: po.Allow, Deny: po.Deny}
								found = true
								break
							}
						}
						if !found {
							session.savedOverwrites[uid] = SavedOverwrite{Exists: false}
						}
					} else {
						session.savedOverwrites[uid] = SavedOverwrite{Exists: false}
					}
				}
				denySpeak := int64(1 << 21)
				if err := s.ChannelPermissionSet(session.ChannelID, uid, discordgo.PermissionOverwriteTypeMember, 0, denySpeak); err == nil {
					session.muted[uid] = true
					// s.ChannelMessageSend(session.ChannelID, fmt.Sprintf("<@%s> さんが割当時間を超えたためミュートしました。", uid))
					embed := &discordgo.MessageEmbed{
						Title:       fmt.Sprintf("時間超過: <@%s> さん", uid),
						Description: fmt.Sprintf("<@%s> さんが割当時間を超えたためミュートしました", uid),
						Color:       0xffff00,
						// Fields: []*discordgo.MessageEmbedField{
						// 	{
						// 		Name:   "項目1",
						// 		Value:  "内容1",
						// 		Inline: true,
						// 	},
						// 	{
						// 		Name:   "項目2",
						// 		Value:  "内容2",
						// 		Inline: true,
						// 	},
						// },
						Footer: &discordgo.MessageEmbedFooter{
							Text: fmt.Sprintf("version: %s", version),
						},
					}
					_, err = s.ChannelMessageSendEmbed(session.ChannelID, embed)
					if err != nil {
						fmt.Println("Embedの送信に失敗しました:", err)
					}
				} else {
					log.Println("ChannelPermissionSet(mute) failed:", err)
				}
			}
		}
		session.mu.Unlock()
	}
}

// HandleVoiceStateUpdate processes VoiceStateUpdate events to immediately
// detect users joining a channel and apply per-channel mute if a session is active.
func (tm *TimerManager) HandleVoiceStateUpdate(s *discordgo.Session, vs *discordgo.VoiceStateUpdate) {
	if vs == nil || vs.UserID == "" {
		return
	}
	// ignore bot
	if vs.UserID == s.State.User.ID {
		return
	}
	if isBot(s, vs.GuildID, vs.UserID) {
		return
	}

	// if user left a channel, ChannelID may be empty — only handle joins
	if vs.ChannelID == "" {
		return
	}

	tm.mu.Lock()
	session, ok := tm.sessions[vs.ChannelID]
	tm.mu.Unlock()
	if !ok {
		return
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	uid := vs.UserID
	if session.participants[uid] {
		return
	}

	// mark as participant with zero allocation and mute in-channel
	session.participants[uid] = true
	session.allocated[uid] = 0
	// save existing overwrite first (best-effort)
	if _, ok := session.savedOverwrites[uid]; !ok {
		ch, err := s.Channel(session.ChannelID)
		if err == nil {
			found := false
			for _, po := range ch.PermissionOverwrites {
				if po.ID == uid && po.Type == discordgo.PermissionOverwriteTypeMember {
					session.savedOverwrites[uid] = SavedOverwrite{Exists: true, Allow: po.Allow, Deny: po.Deny}
					found = true
					break
				}
			}
			if !found {
				session.savedOverwrites[uid] = SavedOverwrite{Exists: false}
			}
		} else {
			session.savedOverwrites[uid] = SavedOverwrite{Exists: false}
		}
	}
	denySpeak := int64(1 << 21)
	if err := s.ChannelPermissionSet(session.ChannelID, uid, discordgo.PermissionOverwriteTypeMember, 0, denySpeak); err != nil {
		log.Println("ChannelPermissionSet(mute on join) failed:", err)
		return
	}
	session.muted[uid] = true
}

// handleTimerCommand is invoked from the interaction handler in main.go
func handleTimerCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	options := i.ApplicationCommandData().Options
	var input string
	for _, opt := range options {
		if opt.Name == optionNameDuration {
			input = opt.StringValue()
			break
		}
	}
	const errMsg = "有効な時間を指定してください。例: 30m, 1h"
	if input == "" {
		sendEphemeral(s, i, errMsg)
		return
	}

	dur, err := time.ParseDuration(input)
	if err != nil {
		sendEphemeral(s, i, errMsg)
		return
	}

	// find the voice channel the invoking user is currently in
	userID := i.Member.User.ID
	guild, _ := s.State.Guild(i.GuildID)
	var channelID string
	if guild != nil {
		for _, vs := range guild.VoiceStates {
			if vs.UserID == userID {
				channelID = vs.ChannelID
				break
			}
		}
	}

	if channelID == "" {
		sendEphemeral(s, i, "まずボイスチャンネルに参加してからコマンドを実行してください。")
		return
	}

	if err := timerManager.StartTimer(s, i.GuildID, channelID, i.ChannelID, dur); err != nil {
		sendEphemeral(s, i, "タイマーの開始に失敗しました: "+err.Error())
		return
	}
	sendEphemeral(s, i, "タイマーを開始します...")
}

// handleStopTimerCommand is invoked from the interaction handler in main.go
func handleStopTimerCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	// find the voice channel the invoking user is currently in
	userID := i.Member.User.ID
	guild, _ := s.State.Guild(i.GuildID)
	var channelID string
	if guild != nil {
		for _, vs := range guild.VoiceStates {
			if vs.UserID == userID {
				channelID = vs.ChannelID
				break
			}
		}
	}

	if channelID == "" {
		sendEphemeral(s, i, "まずボイスチャンネルに参加してからコマンドを実行してください。")
		return
	}

	if err := timerManager.StopTimer(s, i.GuildID, channelID, i.ChannelID); err != nil {
		sendEphemeral(s, i, "タイマーの停止に失敗しました: "+err.Error())
		return
	}
	sendEphemeral(s, i, "タイマーを停止します...")
}

// notifyAdmin sends a plain message to the configured admin channel (best-effort).
func notifyAdmin(s *discordgo.Session, guildID, msg string) {
	// Prefer guild's system channel if available
	if guildID != "" {
		if g, _ := s.State.Guild(guildID); g != nil {
			if g.SystemChannelID != "" {
				if _, err := s.ChannelMessageSend(g.SystemChannelID, msg); err == nil {
					return
				} else {
					log.Println("notifyAdmin: send to system channel failed:", err)
				}
			}
		}
	}
	// Final fallback: log
	log.Println("notifyAdmin: no admin channel available; message:", msg)
}

// isBot attempts to determine whether a user is a bot/accounted-as-app.
// It prefers cached state, falling back to API where necessary. If unknown,
// it returns false (treat as human) to avoid false exclusions.
func isBot(s *discordgo.Session, guildID, userID string) bool {
	// try state member first
	if guildID != "" {
		if m, err := s.State.Member(guildID, userID); err == nil && m != nil && m.User != nil {
			return m.User.Bot
		}
	}
	// fallback to cached user
	if u, err := s.User(userID); err == nil && u != nil {
		return u.Bot
	}
	// if we can't determine, assume not a bot
	return false
}
