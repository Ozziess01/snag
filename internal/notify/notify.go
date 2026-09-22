package notify

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Ozziess01/snag/internal/pipeline"
	"github.com/Ozziess01/snag/internal/store"
)

// Sender — куда уходит готовое сообщение (Telegram или фейк в тестах).
type Sender interface {
	Send(ctx context.Context, chatID, html string) error
}

type Store interface {
	Channels(ctx context.Context, projectID uint64) ([]store.Channel, error)
	Project(ctx context.Context, id uint64) (store.Project, error)
}

// Notifier решает, о чём писать, и отправляет в фоне.
//
// Handle вызывается из воркера после каждой записанной пачки и не должен
// его тормозить: он только раскладывает события и ставит сообщения в
// очередь, а сеть — в Run. Чтобы не заспамить чат:
//   - новые проблемы одной пачки собираются в одно сообщение, а если их
//     больше трёх — в сводку «12 новых проблем»;
//   - про всплеск одной проблемы в один чат пишем не чаще SpikeCooldown.
type Notifier struct {
	Store     Store
	Sender    Sender
	PublicURL string
	Log       *slog.Logger
	Now       func() time.Time

	SpikeCooldown time.Duration

	mu        sync.Mutex
	queue     chan message
	minutes   map[uint64]map[int64]int // проблема → минута → событий
	lastSpike map[[2]uint64]time.Time  // (канал, проблема) → последнее сообщение
	channels  map[uint64]cachedChannels
}

type message struct {
	chatID string
	text   string
}

type cachedChannels struct {
	list    []store.Channel
	project store.Project
	expires time.Time
}

func New(st Store, sender Sender, publicURL string, log *slog.Logger) *Notifier {
	return &Notifier{
		Store: st, Sender: sender, PublicURL: strings.TrimRight(publicURL, "/"), Log: log,
		SpikeCooldown: time.Hour,
		queue:         make(chan message, 1000),
		minutes:       map[uint64]map[int64]int{},
		lastSpike:     map[[2]uint64]time.Time{},
		channels:      map[uint64]cachedChannels{},
	}
}

func (n *Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

// Handle разбирает активность пачки и ставит сообщения в очередь.
func (n *Notifier) Handle(ctx context.Context, activity []pipeline.Activity) {
	now := n.now()
	byProject := map[uint64][]pipeline.Activity{}
	n.mu.Lock()
	for _, a := range activity {
		byProject[a.Issue.ProjectID] = append(byProject[a.Issue.ProjectID], a)
		n.count(a.Issue.ID, now, a.Count)
	}
	n.mu.Unlock()

	for projectID, acts := range byProject {
		chans, project, err := n.projectChannels(ctx, projectID)
		if err != nil {
			n.Log.Error("уведомления: каналы проекта", "project", projectID, "err", err)
			continue
		}
		for _, ch := range chans {
			var created, regressed []pipeline.Activity
			for _, a := range acts {
				switch {
				case a.Issue.Created && ch.OnNew:
					created = append(created, a)
				case a.Issue.Regressed && ch.OnRegression:
					regressed = append(regressed, a)
				}
				if ch.SpikeThreshold > 0 && !a.Issue.Created {
					if total := n.spike(ch, a.Issue.ID, now); total > 0 {
						n.enqueue(ch.Target, n.spikeText(project, a, total, ch.SpikeWindow))
					}
				}
			}
			if len(created) > 0 {
				n.enqueue(ch.Target, n.listText(project, created, "Новая проблема", "новых проблем"))
			}
			if len(regressed) > 0 {
				n.enqueue(ch.Target, n.listText(project, regressed, "Проблема вернулась", "проблем вернулись"))
			}
		}
	}
}

// Run отправляет сообщения из очереди, пока жив контекст. Если Telegram
// просит подождать, ждём и повторяем то же сообщение.
func (n *Notifier) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-n.queue:
			for attempt := 0; attempt < 3; attempt++ {
				err := n.Sender.Send(ctx, m.chatID, m.text)
				var ra RetryAfter
				if errors.As(err, &ra) {
					select {
					case <-time.After(ra.Wait):
						continue
					case <-ctx.Done():
						return
					}
				}
				if err != nil {
					n.Log.Error("уведомление не отправлено", "chat", m.chatID, "err", err)
				}
				break
			}
		}
	}
}

// Test отправляет проверочное сообщение в канал.
func (n *Notifier) Test(ctx context.Context, ch store.Channel, project store.Project) error {
	return n.Sender.Send(ctx, ch.Target, fmt.Sprintf(
		"Snag подключён к проекту <b>%s</b>.\nСюда будут приходить новые и вернувшиеся проблемы%s.",
		html.EscapeString(project.Name), spikeNote(ch)))
}

func spikeNote(ch store.Channel) string {
	if ch.SpikeThreshold == 0 {
		return ""
	}
	return fmt.Sprintf(", а также всплески от %d событий за %d мин", ch.SpikeThreshold, ch.SpikeWindow)
}

func (n *Notifier) enqueue(chatID, text string) {
	select {
	case n.queue <- message{chatID, text}:
	default:
		n.Log.Warn("очередь уведомлений полна, сообщение пропущено", "chat", chatID)
	}
}

// projectChannels кеширует каналы проекта на 30 секунд: Handle вызывается
// на каждую пачку, а настройки меняются редко.
func (n *Notifier) projectChannels(ctx context.Context, projectID uint64) ([]store.Channel, store.Project, error) {
	n.mu.Lock()
	c, ok := n.channels[projectID]
	n.mu.Unlock()
	if ok && n.now().Before(c.expires) {
		return c.list, c.project, nil
	}
	list, err := n.Store.Channels(ctx, projectID)
	if err != nil {
		return nil, store.Project{}, err
	}
	project, err := n.Store.Project(ctx, projectID)
	if err != nil {
		return nil, store.Project{}, err
	}
	n.mu.Lock()
	n.channels[projectID] = cachedChannels{list: list, project: project, expires: n.now().Add(30 * time.Second)}
	n.mu.Unlock()
	return list, project, nil
}

// Forget сбрасывает кеш каналов проекта (после изменения настроек).
func (n *Notifier) Forget(projectID uint64) {
	n.mu.Lock()
	delete(n.channels, projectID)
	n.mu.Unlock()
}

// ---------- всплески ----------

// count добавляет события проблемы в поминутный счётчик за последний час.
func (n *Notifier) count(issueID uint64, now time.Time, events int) {
	m := n.minutes[issueID]
	if m == nil {
		m = map[int64]int{}
		n.minutes[issueID] = m
	}
	minute := now.Unix() / 60
	m[minute] += events
	for k := range m {
		if k < minute-60 {
			delete(m, k)
		}
	}
}

// spike — сколько событий проблемы за окно канала, если это всплеск и про
// него ещё не писали в последние SpikeCooldown; иначе 0.
func (n *Notifier) spike(ch store.Channel, issueID uint64, now time.Time) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	minute := now.Unix() / 60
	total := 0
	for k, v := range n.minutes[issueID] {
		if k > minute-int64(ch.SpikeWindow) {
			total += v
		}
	}
	if total < ch.SpikeThreshold {
		return 0
	}
	key := [2]uint64{ch.ID, issueID}
	if last, ok := n.lastSpike[key]; ok && now.Sub(last) < n.SpikeCooldown {
		return 0
	}
	n.lastSpike[key] = now
	return total
}

// ---------- тексты ----------

func (n *Notifier) link(issueID uint64) string {
	return n.PublicURL + "/#/issues/" + strconv.FormatUint(issueID, 10)
}

func (n *Notifier) issueLine(a pipeline.Activity) string {
	title := html.EscapeString(a.Event.Title())
	if typ, rest, ok := strings.Cut(title, ": "); ok && !strings.Contains(typ, " ") {
		title = "<b>" + typ + "</b>: " + rest
	}
	line := title
	if c := a.Event.Culprit(); c != "" {
		line += "\n<code>" + html.EscapeString(c) + "</code>"
	}
	return line
}

func (n *Notifier) meta(a pipeline.Activity) string {
	var parts []string
	if a.Event.Environment != "" {
		parts = append(parts, html.EscapeString(a.Event.Environment))
	}
	if a.Event.Release != "" {
		parts = append(parts, html.EscapeString(a.Event.Release))
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n" + strings.Join(parts, " · ")
}

func (n *Notifier) listText(project store.Project, acts []pipeline.Activity, one, many string) string {
	name := html.EscapeString(project.Name)
	if len(acts) == 1 {
		a := acts[0]
		return fmt.Sprintf("%s · %s\n\n%s%s\n\n<a href=\"%s\">Открыть в Snag</a>", one, name, n.issueLine(a), n.meta(a), n.link(a.Issue.ID))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s · %s\n", len(acts), many, name)
	shown := acts
	if len(shown) > 5 {
		shown = shown[:5]
	}
	for _, a := range shown {
		fmt.Fprintf(&b, "\n• <a href=\"%s\">%s</a>", n.link(a.Issue.ID), html.EscapeString(a.Event.Title()))
	}
	if rest := len(acts) - len(shown); rest > 0 {
		fmt.Fprintf(&b, "\n\nи ещё %d", rest)
	}
	return b.String()
}

func (n *Notifier) spikeText(project store.Project, a pipeline.Activity, total, window int) string {
	return fmt.Sprintf("Всплеск · %s\n\n%s\n\n%d событий за %d мин\n\n<a href=\"%s\">Открыть в Snag</a>",
		html.EscapeString(project.Name), n.issueLine(a), total, window, n.link(a.Issue.ID))
}
