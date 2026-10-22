// Генератор событий прослушивания для лаборатории Kafka + ClickHouse.
//
// Модель: треки разбиты на "вайб-кластеры". Внутри сессии пользователь
// чаще переходит к треку из того же кластера и реже его скипает.
// Поэтому матрица переходов в ClickHouse получается осмысленной, а не шумом.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
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

type session struct {
	id        string
	user      string
	prevTrack int // -1, если трек первый
	left      int // сколько событий осталось до конца сессии
}

type generator struct {
	rnd      *rand.Rand
	tracks   int
	clusters int
	sessions []*session
	seq      uint64
}

func newGenerator(seed int64, users, tracks, clusters int) *generator {
	g := &generator{
		rnd:      rand.New(rand.NewSource(seed)),
		tracks:   tracks,
		clusters: clusters,
	}
	for i := 0; i < users; i++ {
		g.sessions = append(g.sessions, g.newSession(i))
	}
	return g
}

func (g *generator) newSession(user int) *session {
	g.seq++
	return &session{
		id:        fmt.Sprintf("s%d-%d", user, g.seq),
		user:      fmt.Sprintf("u%d", user),
		prevTrack: -1,
		left:      15 + g.rnd.Intn(30),
	}
}

func (g *generator) cluster(track int) int { return track % g.clusters }

// nextTrack: 70% остаёмся в кластере предыдущего трека, иначе случайный трек.
func (g *generator) nextTrack(prev int) int {
	if prev >= 0 && g.rnd.Float64() < 0.7 {
		c := g.cluster(prev)
		n := (g.tracks - c + g.clusters - 1) / g.clusters // треков в кластере
		return c + g.rnd.Intn(n)*g.clusters
	}
	return g.rnd.Intn(g.tracks)
}

func (g *generator) next() Event {
	idx := g.rnd.Intn(len(g.sessions))
	s := g.sessions[idx]

	track := g.nextTrack(s.prevTrack)
	sameVibe := s.prevTrack >= 0 && g.cluster(track) == g.cluster(s.prevTrack)

	skipProb := 0.6
	if sameVibe {
		skipProb = 0.15
	}

	ev := Event{
		TsMs:      time.Now().UnixMilli(),
		UserID:    s.user,
		SessionID: s.id,
		TrackID:   fmt.Sprintf("t%04d", track),
	}
	if s.prevTrack >= 0 {
		ev.PrevTrackID = fmt.Sprintf("t%04d", s.prevTrack)
	}

	switch {
	case g.rnd.Float64() < skipProb:
		ev.EventType = "skip"
		ev.ListenMs = 3000 + g.rnd.Intn(27000)
	case g.rnd.Float64() < 0.06:
		ev.EventType = "like"
		ev.ListenMs = 150000 + g.rnd.Intn(90000)
	default:
		ev.EventType = "complete"
		ev.ListenMs = 150000 + g.rnd.Intn(90000)
	}
	g.seq++
	ev.EventID = fmt.Sprintf("%s-%d", s.id, g.seq)

	s.prevTrack = track
	s.left--
	if s.left <= 0 {
		g.sessions[idx] = g.newSession(idx)
	}
	return ev
}

func main() {
	var (
		brokers  = flag.String("brokers", "localhost:9092", "список брокеров через запятую")
		topic    = flag.String("topic", "vibe.events.stream", "топик")
		rate     = flag.Int("rate", 500, "событий в секунду")
		duration = flag.Duration("duration", 30*time.Second, "сколько работать (0 = до Ctrl+C)")
		users    = flag.Int("users", 200, "число одновременных сессий")
		tracks   = flag.Int("tracks", 2000, "число треков")
		clusters = flag.Int("clusters", 12, "число вайб-кластеров")
		seed     = flag.Int64("seed", time.Now().UnixNano(), "seed генератора")
	)
	flag.Parse()

	if *rate <= 0 || *users <= 0 || *tracks < *clusters || *clusters <= 0 {
		log.Fatal("некорректные параметры: rate>0, users>0, clusters>0, tracks>=clusters")
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

	gen := newGenerator(*seed, *users, *tracks, *clusters)
	var sent, failed atomic.Int64

	log.Printf("старт: %d событий/с, топик %s, брокеры %s", *rate, *topic, *brokers)

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
				ev := gen.next()
				b, _ := json.Marshal(ev)
				// ключ = user_id: события одного пользователя идут в одну партицию по порядку
				cl.Produce(context.Background(), &kgo.Record{Key: []byte(ev.UserID), Value: b},
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
