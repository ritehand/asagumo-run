package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

type TimerSession struct {
	GuildID   string
	ChannelID string
	Total     time.Duration
	Start     time.Time

	userSpeakingTime map[string]time.Duration
	lastStart        map[string]time.Time
	allocated        map[string]time.Duration
	participants     map[string]bool
	muted            map[string]bool
	timers           map[string]*time.Timer

	mu     sync.Mutex
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
				participants = append(participants, vs.UserID)
			}
		}
	}

	if len(participants) == 0 {
		return fmt.Errorf("ボイスチャンネルに参加しているユーザーがいません")
	}

	session := &TimerSession{
		GuildID:          guildID,
		ChannelID:        channelID,
		Total:            total,
		Start:            time.Now(),
		userSpeakingTime: make(map[string]time.Duration),
		lastStart:        make(map[string]time.Time),
		allocated:        make(map[string]time.Duration),
		participants:     make(map[string]bool),
		muted:            make(map[string]bool),
		timers:           make(map[string]*time.Timer),
		Active:           true,
	}

	per := total / time.Duration(len(participants))
	for _, u := range participants {
		session.participants[u] = true
		session.allocated[u] = per
	}

	tm.sessions[channelID] = session

	s.ChannelMessageSend(replyChannelID, fmt.Sprintf("タイマーを開始しました。合計 %v、参加者 %d、各自割当 %v", total, len(participants), per))

	go session.monitorTotal(s, replyChannelID)
	return nil
}

func (ts *TimerSession) monitorTotal(s *discordgo.Session, replyChannelID string) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		ts.mu.Lock()
		// compute total used
		var sum time.Duration
		for uid, d := range ts.userSpeakingTime {
			sum += d
			// if currently speaking, include running segment
			if start, ok := ts.lastStart[uid]; ok && !start.IsZero() {
				sum += time.Since(start)
			}
		}
		// also consider users who have lastStart but no userSpeakingTime entry yet
		for uid, start := range ts.lastStart {
			if start.IsZero() {
				continue
			}
			if _, ok := ts.userSpeakingTime[uid]; !ok {
				sum += time.Since(start)
			}
		}

		if sum >= ts.Total {
			ts.Active = false
			// before unlocking, capture participants to unmute
			participants := make([]string, 0, len(ts.participants))
			for uid := range ts.participants {
				participants = append(participants, uid)
			}
			ts.mu.Unlock()

			s.ChannelMessageSend(replyChannelID, "全体の持ち時間が終了しました。ミュート設定を解除します。")
			// remove per-channel permission overwrites we added
			for _, uid := range participants {
				if ts.muted[uid] {
					if err := s.ChannelPermissionDelete(ts.ChannelID, uid); err != nil {
						log.Println("ChannelPermissionDelete(unmute) failed:", err)
					}
				}
			}

			// remove session
			timerManager.mu.Lock()
			delete(timerManager.sessions, ts.ChannelID)
			timerManager.mu.Unlock()
			return
		}
		ts.mu.Unlock()
	}
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
		// process only if the user is tracked in this session
		// If user is not part of participants set, they may have joined mid-session.
		if !session.participants[uid] {
			// enforce per-channel mute for late joiners
			session.participants[uid] = true
			session.allocated[uid] = 0
			// deny SPEAK permission in this channel (bit 1<<21)
			denySpeak := int64(1 << 21)
			if err := s.ChannelPermissionSet(session.ChannelID, uid, discordgo.PermissionOverwriteTypeMember, 0, denySpeak); err != nil {
				log.Println("ChannelPermissionSet(mute late join) failed:", err)
			} else {
				session.muted[uid] = true
				s.ChannelMessageSend(session.ChannelID, fmt.Sprintf("<@%s> さんは途中参加のためミュートしました。", uid))
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
					// immediate mute
					denySpeak := int64(1 << 21)
					if err := s.ChannelPermissionSet(session.ChannelID, uid, discordgo.PermissionOverwriteTypeMember, 0, denySpeak); err == nil {
						session.muted[uid] = true
						s.ChannelMessageSend(session.ChannelID, fmt.Sprintf("<@%s> さんが割当時間を超えたためミュートしました。", uid))
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
					session.muted[uidCopy] = true
					session.mu.Unlock()

					// apply channel permission mute
					denySpeak := int64(1 << 21)
					if err := s.ChannelPermissionSet(chID, uidCopy, discordgo.PermissionOverwriteTypeMember, 0, denySpeak); err == nil {
						s.ChannelMessageSend(chID, fmt.Sprintf("<@%s> さんが割当時間を超えたためミュートしました。", uidCopy))
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
				// set per-channel permission overwrite to deny speaking
				denySpeak := int64(1 << 21)
				if err := s.ChannelPermissionSet(session.ChannelID, uid, discordgo.PermissionOverwriteTypeMember, 0, denySpeak); err == nil {
					session.muted[uid] = true
					s.ChannelMessageSend(session.ChannelID, fmt.Sprintf("<@%s> さんが割当時間を超えたためミュートしました。", uid))
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
	denySpeak := int64(1 << 21)
	if err := s.ChannelPermissionSet(session.ChannelID, uid, discordgo.PermissionOverwriteTypeMember, 0, denySpeak); err != nil {
		log.Println("ChannelPermissionSet(mute on join) failed:", err)
		return
	}
	session.muted[uid] = true
	// s.ChannelMessageSend(session.ChannelID, fmt.Sprintf("<@%s> さんが途中参加したためミュートしました。", uid))
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

	sendEphemeral(s, i, fmt.Sprintf("タイマーを開始しました: %v", dur))
}
