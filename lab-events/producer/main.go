// Генератор событий прослушивания для лаборатории Kafka + ClickHouse.
//
// Данные СИНТЕТИЧЕСКИЕ. Треки настоящие (FMA small из pipeline/output/
// points.json), но слушатели и их поведение выдуманы: жанр трека служит
// «кластером», и внутри сессии пользователь чаще переходит к треку того же
// жанра и реже его скипает. Поэтому матрица переходов в ClickHouse
// получается осмысленной, а не шумом, но ничего не говорит о реальных людях.
//
// Популярность треков распределена по Ципфу (-zipf): немногие треки слушают
// часто, хвост — редко, как в реальном каталоге. Без этого при 8000 треков
// пары переходов почти не повторяются и матрица остаётся пустой.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	mrand "math/rand"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

type Event struct {
	EventID     string `json:"event_id"`
	TsMs        int64  `json:"ts_ms"`
	UserID      string `json:"user_id"`
	SessionID   string `json:"session_id"`
	TrackID     string `json:"track_id"`
	PrevTrackID string `json:"prev_track_id"`
	EventType   string `json:"event_type"` // complete | skip | like
	ListenMs    int    `json:"listen_ms"`
}

// track — точка из points.json (координаты x, y генератору не нужны).
type track struct {
	ID    int64  `json:"track_id"`
	Genre string `json:"genre"`
}

// loadTracks читает points.json, который пишет pipeline/prepare_data.py.
func loadTracks(path string) ([]track, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tracks []track
	if err := json.Unmarshal(raw, &tracks); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(tracks) < 2 {
		return nil, fmt.Errorf("%s: нужно хотя бы 2 трека, найдено %d", path, len(tracks))
	}
	return tracks, nil
}

type session struct {
	id        string
	user      string
	prevTrack int // индекс в generator.tracks; -1, если трек первый
	left      int // сколько треков осталось до конца сессии
}

type generator struct {
	rnd      *mrand.Rand
	tracks   []track
	byGenre  map[string]*picker // жанр -> треки жанра
	all      *picker
	sessions []*session
	seq      uint64
}

// picker выбирает трек из списка: по Ципфу (первые в списке — самые
// популярные) или равномерно, если zipf == nil.
type picker struct {
	idx  []int
	zipf *mrand.Zipf
}

func newPicker(rnd *mrand.Rand, idx []int, s float64) *picker {
	p := &picker{idx: idx}
	if s > 1 && len(idx) > 1 {
		// v = 4 сглаживает голову: при v = 1 первый трек жанра забирал ~25%
		// выборов и матрицу заполняли его повторы, а при v = 10 пары почти не
		// повторялись (подобрано симуляцией, см. README).
		p.zipf = mrand.NewZipf(rnd, s, 4, uint64(len(idx)-1))
	}
	return p
}

func (p *picker) pick(rnd *mrand.Rand) int {
	if p.zipf == nil {
		return p.idx[rnd.Intn(len(p.idx))]
	}
	return p.idx[p.zipf.Uint64()]
}

func newGenerator(seed int64, users int, tracks []track, zipf float64) *generator {
	rnd := mrand.New(mrand.NewSource(seed))
	g := &generator{rnd: rnd, tracks: tracks, byGenre: make(map[string]*picker)}

	// Один общий порядок популярности: трек, популярный в целом, популярен
	// и внутри своего жанра.
	order := rnd.Perm(len(tracks))
	genres := make(map[string][]int)
	for _, i := range order {
		genres[tracks[i].Genre] = append(genres[tracks[i].Genre], i)
	}
	g.all = newPicker(rnd, order, zipf)
	for genre, idx := range genres {
		g.byGenre[genre] = newPicker(rnd, idx, zipf)
	}
	for i := 0; i < users; i++ {
		g.sessions = append(g.sessions, g.newSession(i))
	}
	return g
}

func (g *generator) newSession(user int) *session {
	g.seq++
	return &session{
		id:        fmt.Sprintf("sess-%d-%d", user, g.seq),
		user:      fmt.Sprintf("u%d", user),
		prevTrack: -1,
		left:      15 + g.rnd.Intn(30),
	}
}

// nextTrack: 70% остаёмся в жанре предыдущего трека, иначе случайный трек.
// Тот же трек подряд не повторяется.
func (g *generator) nextTrack(prev int) int {
	from := g.all
	if prev >= 0 && g.rnd.Float64() < 0.7 {
		from = g.byGenre[g.tracks[prev].Genre]
	}
	for {
		if next := from.pick(g.rnd); next != prev {
			return next
		}
	}
}

// next выдаёт события одного прослушивания: skip или complete, а после
// complete иногда ещё like (отдельным событием, как его пришлёт плеер).
func (g *generator) next() []Event {
	idx := g.rnd.Intn(len(g.sessions))
	s := g.sessions[idx]

	cur := g.nextTrack(s.prevTrack)
	sameVibe := s.prevTrack >= 0 && g.tracks[cur].Genre == g.tracks[s.prevTrack].Genre

	skipProb := 0.6
	if sameVibe {
		skipProb = 0.15
	}

	ev := Event{
		EventID:   newUUID(),
		TsMs:      time.Now().UnixMilli(),
		UserID:    s.user,
		SessionID: s.id,
		TrackID:   strconv.FormatInt(g.tracks[cur].ID, 10),
	}
	if s.prevTrack >= 0 {
		ev.PrevTrackID = strconv.FormatInt(g.tracks[s.prevTrack].ID, 10)
	}

	out := []Event{ev}
	if g.rnd.Float64() < skipProb {
		out[0].EventType = "skip"
		out[0].ListenMs = 3000 + g.rnd.Intn(27000)
	} else {
		out[0].EventType = "complete"
		out[0].ListenMs = 150000 + g.rnd.Intn(90000)
		if g.rnd.Float64() < 0.06 {
			like := ev
			like.EventID = newUUID()
			like.EventType = "like"
			like.ListenMs = g.rnd.Intn(out[0].ListenMs) // момент лайка внутри трека
			out = append(out, like)
		}
	}

	s.prevTrack = cur
	s.left--
	if s.left <= 0 {
		g.sessions[idx] = g.newSession(idx)
	}
	return out
}

// newUUID — случайный UUID v4, как event_id из API.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func main() {
	var (
		brokers  = flag.String("brokers", "localhost:9092", "список брокеров через запятую")
		topic    = flag.String("topic", "vibe.events.stream", "топик")
		rate     = flag.Int("rate", 500, "событий в секунду")
		duration = flag.Duration("duration", 30*time.Second, "сколько работать (0 = до Ctrl+C)")
		users    = flag.Int("users", 200, "число одновременных сессий")
		points   = flag.String("points", "../../pipeline/output/points.json", "треки: points.json из pipeline")
		zipf     = flag.Float64("zipf", 1.2, "показатель Ципфа для популярности треков (<=1 — равномерно)")
		seed     = flag.Int64("seed", time.Now().UnixNano(), "seed генератора")
	)
	flag.Parse()

	if *rate <= 0 || *users <= 0 {
		log.Fatal("некорректные параметры: rate>0, users>0")
	}
	tracks, err := loadTracks(*points)
	if err != nil {
		log.Fatalf("треки: %v (запустите pipeline/prepare_data.py или укажите -points)", err)
	}

	cl, err := kgo.NewClient(
		kgo.SeedBrokers(splitComma(*brokers)...),
		kgo.DefaultProduceTopic(*topic),
		kgo.ProducerLinger(20*time.Millisecond),
	)
	if err != nil {
		log.Fatalf("kafka client: %v", err)
	}
	defer cl.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	pingCtx, cancelPing := context.WithTimeout(ctx, 10*time.Second)
	if err := cl.Ping(pingCtx); err != nil {
		cancelPing()
		log.Fatalf("брокер недоступен (%s): %v", *brokers, err)
	}
	cancelPing()

	gen := newGenerator(*seed, *users, tracks, *zipf)
	var sent, failed atomic.Int64

	log.Printf("старт: %d прослушиваний/с, %d треков, %d жанров, топик %s, брокеры %s",
		*rate, len(tracks), len(gen.byGenre), *topic, *brokers)

	const tick = 100 * time.Millisecond
	perTick := float64(*rate) * tick.Seconds()
	var carry float64
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	report := time.NewTicker(5 * time.Second)
	defer report.Stop()

loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-report.C:
			log.Printf("отправлено: %d, ошибок: %d", sent.Load(), failed.Load())
		case <-ticker.C:
			carry += perTick
			n := int(carry)
			carry -= float64(n)
			for i := 0; i < n; i++ {
				for _, ev := range gen.next() {
					b, _ := json.Marshal(ev)
					// ключ = session_id, как в API: события сессии идут в одну партицию по порядку
					cl.Produce(context.Background(), &kgo.Record{Key: []byte(ev.SessionID), Value: b},
						func(_ *kgo.Record, err error) {
							if err != nil {
								failed.Add(1)
								return
							}
							sent.Add(1)
						})
				}
			}
		}
	}

	flushCtx, cancelFlush := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelFlush()
	if err := cl.Flush(flushCtx); err != nil {
		log.Printf("flush: %v", err)
	}
	log.Printf("готово: отправлено %d, ошибок %d", sent.Load(), failed.Load())
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}
