# 1. ボイスチャンネルで使えるコマンド `timer`

ボイスチャンネルの時間制限機能を作りたい。
全体の持ち時間をコマンドで設定 (30mなど)。コマンドを叩くとタイマースタート（チャンネルのチャットにメッセージ）。

全体の持ち時間 / 現在入室している人数 (Bot除く) = 各自に割り当てられた時間

ユーザーが話している間、そのユーザーに割り当てられた時間が減っていく。それを超えて話そうとするとそのチャンネル内に限り強制ミュート（チャンネルのチャットにメッセージ）。
コマンド実行中に途中入室してきたユーザーは強制ミュート。
全体の持ち時間に達するとコマンド終了。強制ミュートされた人も元に戻って話せるようになる（チャンネルのチャットにメッセージ）。

## TODO

- [x] 基本機能実装
- [x] チャンネル個別の権限でのミュート（チャンネル内のみ）に変更
- [x] 途中入室ユーザーの即時検出（参加時に自動ミュート）
- [ ] 発言中のリアルタイム制御（即時停止）

## 以下、コードスニペット。あくまで参考に。

```go
// ユーザーをミュートする関数
func muteUser(s *discordgo.Session, guildID, userID string) {
    trueVar := true
    err := s.GuildMemberEdit(guildID, userID, &discordgo.GuildMemberParams{
        Mute: &trueVar,
    })
    if err != nil {
        log.Println("ミュートに失敗しました:", err)
    }
}

// タイマー開始の例（コマンドハンドラ内など）
func startLimitTimer(s *discordgo.Session, m *discordgo.MessageCreate, userID string, duration time.Duration) {
    // duration後に実行されるタイマー
    time.AfterFunc(duration, func() {
        muteUser(s, m.GuildID, userID)
        s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("<@%s> さんの持ち時間が終了したため、ミュートしました。", userID))
    })
}
```

```go
var userSpeakingTime = make(map[string]time.Duration)
var lastStart = make(map[string]time.Time)

// ボイス状態（発言開始・終了）を検知するハンドラ
dg.AddHandler(func(s *discordgo.Session, v *discordgo.VoiceSpeakingUpdate) {
    if v.UserID != targetUserID { return }

    if v.Speaking {
        // 話し始めた時刻を記録
        lastStart[v.UserID] = time.Now()
    } else {
        // 話し終わったら累積時間に加算
        if start, ok := lastStart[v.UserID]; ok {
            duration := time.Since(start)
            userSpeakingTime[v.UserID] += duration
            
            // 制限時間を超えたかチェック
            if userSpeakingTime[v.UserID] > limitDuration {
                restrictSpeech(s, v.GuildID, currentChannelID, v.UserID)
            }
        }
    }
})
```

```go
func restrictSpeech(s *discordgo.Session, guildID, channelID, userID string) {
    // チャンネルの権限を上書きして、そのユーザーだけ「話す」を禁止にする
    err := s.ChannelPermissionSet(channelID, userID, discordgo.PermissionOverwriteTypeMember, 0, discordgo.PermissionSpeak)
    if err != nil {
        log.Println("権限設定に失敗しました:", err)
    }
}
```

```go
func restrictSpeechWithAutoRelease(s *discordgo.Session, channelID, userID string, muteDuration time.Duration) {
    // 1. そのチャンネルだけで「話す」を禁止にする
    err := s.ChannelPermissionSet(channelID, userID, discordgo.PermissionOverwriteTypeMember, 0, discordgo.PermissionSpeak)
    if err != nil {
        log.Println("ミュート設定に失敗:", err)
        return
    }
    
    s.ChannelMessageSend(channelID, fmt.Sprintf("<@%s> さんの持ち時間が終了したため、%v 間のミュートを開始しました。", userID, muteDuration))

    // 2. 指定時間後に実行される「解除タイマー」を予約する
    time.AfterFunc(muteDuration, func() {
        // 3. チャンネルの個別権限設定を削除（＝ミュート解除）
        err := s.ChannelPermissionDelete(channelID, userID)
        if err != nil {
            log.Println("ミュート解除に失敗:", err)
            return
        }
        s.ChannelMessageSend(channelID, fmt.Sprintf("<@%s> さんのミュート期間が終了しました。発言可能です。", userID))
    })
}
```