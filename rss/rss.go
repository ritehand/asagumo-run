package rss

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/mmcdole/gofeed"
)

// 日付が取れないアイテムのGUID重複排除で、フィードごとに保持する件数の上限。
// 無限にメモリが増えないよう、古いものから捨てる(FIFO)。
const maxTrackedGUIDsPerFeed = 100

// ---- 設定 ----

type FeedConfig struct {
	Tag      string
	URL      string
	Interval time.Duration
}

// ---- ランタイム状態（プロセスのメモリ上のみ。再起動すれば消える） ----
//
// 永続化はせず、以下の2つだけをメモリ上で覚えておく。
//   1) LastSeen: このフィードで最後に配信した記事の公開日時
//      → 起動時刻より古い記事を無視し、同じ記事を何度も配信しない
//   2) seenGUIDs: 公開日時が取れないアイテム専用の軽量な重複排除セット
//      （日付判定ができないアイテムの保険。件数上限つきFIFO）

type FeedState struct {
	mu           sync.Mutex
	ETag         string
	LastModified string
	LastSeen     time.Time // これより新しい公開日時の記事だけを「新規」とみなす

	seenGUIDs map[string]struct{} // 日付なしアイテム用の重複排除セット
	guidOrder []string            // 上限を超えたら古いものから捨てるためのFIFO順
}

type StateMap struct {
	mu   sync.Mutex
	data map[string]*FeedState
}

func NewStateMap() *StateMap {
	return &StateMap{data: map[string]*FeedState{}}
}

// Get は初回アクセス時に LastSeen を startTime で初期化する。
// これが「起動時点より古い記事は無視する」の実体。
func (s *StateMap) Get(url string, startTime time.Time) *FeedState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.data[url]
	if !ok {
		st = &FeedState{
			LastSeen:  startTime,
			seenGUIDs: map[string]struct{}{},
		}
		s.data[url] = st
	}
	return st
}

// markGUIDSeen は日付なしアイテム用の重複排除セットに記録する。
// 呼び出し元で state.mu をロック済みであることが前提。
// 上限(maxTrackedGUIDsPerFeed)を超えたら古いものから捨てる。
func (st *FeedState) markGUIDSeen(guid string) {
	st.seenGUIDs[guid] = struct{}{}
	st.guidOrder = append(st.guidOrder, guid)
	if len(st.guidOrder) > maxTrackedGUIDsPerFeed {
		oldest := st.guidOrder[0]
		st.guidOrder = st.guidOrder[1:]
		delete(st.seenGUIDs, oldest)
	}
}

// itemGUIDKey は日付判定に使えないアイテムの識別キーを作る。
// GUID→Link→Title+Publishedの順でフォールバックし、短いハッシュに丸める。
func itemGUIDKey(it *gofeed.Item) string {
	key := it.GUID
	if key == "" {
		key = it.Link
	}
	if key == "" {
		key = it.Title + it.Published
	}
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// ---- Watcher本体 ----

// OnUpdate は新規アイテムを検知するたびに呼ばれる「特定のfunc」。
type OnUpdate func(feed FeedConfig, item *gofeed.Item)

type Watcher struct {
	client    *http.Client
	parser    *gofeed.Parser
	states    *StateMap
	startTime time.Time     // これより古い記事は無視する基準時刻
	sem       chan struct{} // 同時実行数の上限（VPSのCPU/帯域/相手サイトへの配慮）
	onUpdate  OnUpdate
}

func NewWatcher(maxConcurrent int, onUpdate OnUpdate) *Watcher {
	return &Watcher{
		client:    &http.Client{Timeout: 15 * time.Second},
		parser:    gofeed.NewParser(),
		states:    NewStateMap(),
		startTime: time.Now(),
		sem:       make(chan struct{}, maxConcurrent),
		onUpdate:  onUpdate,
	}
}

// itemPublished は記事の公開日時を取り出す。取得できなければ nil。
// pubDate が無い/壊れているフィードは意外と多いので、Updated も fallback に見る。
func itemPublished(it *gofeed.Item) *time.Time {
	if it.PublishedParsed != nil {
		return it.PublishedParsed
	}
	if it.UpdatedParsed != nil {
		return it.UpdatedParsed
	}
	return nil
}

func (w *Watcher) poll(ctx context.Context, feed FeedConfig) {
	w.sem <- struct{}{}
	defer func() { <-w.sem }()

	state := w.states.Get(feed.URL, w.startTime)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feed.URL, nil)
	if err != nil {
		log.Printf("[%s] request build error: %v", feed.URL, err)
		return
	}
	// 条件付きGETで「更新なし」なら304を返してもらい、帯域とパース処理を節約する
	if state.ETag != "" {
		req.Header.Set("If-None-Match", state.ETag)
	}
	if state.LastModified != "" {
		req.Header.Set("If-Modified-Since", state.LastModified)
	}
	req.Header.Set("User-Agent", "rss-watcher/1.0 (+contact@example.com)")

	resp, err := w.client.Do(req)
	if err != nil {
		log.Printf("[%s] fetch error: %v", feed.URL, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return // 更新なし
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("[%s] unexpected status: %d", feed.URL, resp.StatusCode)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[%s] read error: %v", feed.URL, err)
		return
	}

	f, err := w.parser.ParseString(string(body))
	if err != nil {
		log.Printf("[%s] parse error: %v", feed.URL, err)
		return
	}

	state.mu.Lock()
	maxSeen := state.LastSeen
	for _, item := range f.Items {
		pub := itemPublished(item)
		if pub == nil {
			// 公開日時が取れないアイテムはGUID(相当)ベースの軽量な重複排除にフォールバック
			key := itemGUIDKey(item)
			if _, seen := state.seenGUIDs[key]; seen {
				continue
			}
			state.markGUIDSeen(key)
			w.onUpdate(feed, item) // ← ここで「特定のfunc」を実行
			continue
		}
		if !pub.After(state.LastSeen) {
			continue // 起動時点より古い、または配信済み
		}
		w.onUpdate(feed, item) // ← ここで「特定のfunc」を実行
		if pub.After(maxSeen) {
			maxSeen = *pub
		}
	}
	state.LastSeen = maxSeen
	state.ETag = resp.Header.Get("ETag")
	state.LastModified = resp.Header.Get("Last-Modified")
	state.mu.Unlock()
}

// Run は各フィードごとに独立したgoroutineとtickerを持たせて巡回する。
// フィードごとに間隔を変えられるので「更新頻度が高いサイトは短く、低いサイトは長く」調整できる。
func (w *Watcher) Run(ctx context.Context, feeds []FeedConfig) {
	var wg sync.WaitGroup
	for _, f := range feeds {
		wg.Add(1)
		go func(f FeedConfig) {
			defer wg.Done()

			// 起動直後に全フィードへ一斉アクセスしないようジッターを入れる
			jitter := time.Duration(rand.Int63n(int64(f.Interval)))
			select {
			case <-time.After(jitter):
			case <-ctx.Done():
				return
			}

			ticker := time.NewTicker(f.Interval)
			defer ticker.Stop()

			w.poll(ctx, f)
			for {
				select {
				case <-ticker.C:
					w.poll(ctx, f)
				case <-ctx.Done():
					return
				}
			}
		}(f)
	}
	wg.Wait()
}

// ---- エントリポイント ----

// func main() {
// 	// 実際は設定ファイル(YAML/JSON)やDBから読み込む想定
// 	feeds := []rss.FeedConfig{
// 		{URL: "https://example.com/feed1.xml", Interval: 5 * time.Minute},
// 		{URL: "https://example.com/feed2.xml", Interval: 10 * time.Minute},
// 		// ... 数百〜数千件追加してもgoroutineは軽量なので問題ない
// 	}

// 	onUpdate := func(feedURL rss.FeedConfig, item *gofeed.Item) {
// 		// ここが「更新があれば回す特定のfunc」
// 		fmt.Printf("[NEW] %s: %s (%s)\n", feedURL, item.Title, item.Link)
// 	}

// 	watcher := rss.NewWatcher(20, onUpdate) // 同時に叩きに行くのは最大20フィードまで

// 	ctx, cancel := context.WithCancel(context.Background())
// 	sigCh := make(chan os.Signal, 1)
// 	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
// 	go func() {
// 		<-sigCh
// 		log.Println("shutting down...")
// 		cancel()
// 	}()

// 	watcher.Run(ctx, feeds)
// }
