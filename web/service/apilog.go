package service

import (
	"errors"
	"sync"
	"time"

	"gorm.io/gorm"

	"nexcore-x-ui/database"
	"nexcore-x-ui/database/model"
)

const (
	// apiLogGCAfter — log rows older than this are dropped by the cron.
	// 14 days is a comfortable forensic window without filling sqlite
	// for high-QPS deployments.
	apiLogGCAfter = 14 * 24 * time.Hour
	// purgeChunkSize — DELETE this many rows per pass. Keeps the write
	// lock duration tight even when there are millions of expired rows
	// to evict (e.g. operator catching up after a long downtime).
	purgeChunkSize = 1000
	// recordFlushInterval — how often the buffer worker drains the
	// pending log rows into a single batch INSERT. Anything in flight
	// at process exit is lost; that's acceptable for a request log.
	recordFlushInterval = 5 * time.Second
	// recordFlushThreshold — flush early when the buffer fills, so a
	// burst of traffic doesn't sit unwritten for the full interval.
	recordFlushThreshold = 200
	// recordBufferCap — hard ceiling. Once exceeded we drop oldest
	// pending entries (FIFO) instead of letting the buffer eat memory
	// when the DB write path is stuck.
	recordBufferCap = 5000
)

type APILogService struct{}

// Record stages a row in an in-memory buffer that a single background
// worker flushes via batched INSERT. The previous design forked one
// goroutine per request, which at 1000 QPS is 1000 goroutines/sec
// and 1000 individual INSERTs — both of which sqlite hates more than
// you'd think.
func (s *APILogService) Record(entry *model.APILog) {
	if entry == nil {
		return
	}
	if entry.At == 0 {
		entry.At = time.Now().Unix()
	}
	logBuf.add(entry)
}

// ---- async log buffer ----

var logBuf = newLogBuffer()

type logBuffer struct {
	mu     sync.Mutex
	queue  []*model.APILog
	once   sync.Once
}

func newLogBuffer() *logBuffer {
	return &logBuffer{queue: make([]*model.APILog, 0, recordFlushThreshold)}
}

func (b *logBuffer) add(e *model.APILog) {
	b.once.Do(b.startFlusher)
	b.mu.Lock()
	if len(b.queue) >= recordBufferCap {
		// Drop the oldest entry rather than blocking the request path.
		// Better to lose a log record than to have apilog backpressure
		// the API itself.
		b.queue = b.queue[1:]
	}
	b.queue = append(b.queue, e)
	if len(b.queue) >= recordFlushThreshold {
		batch := b.queue
		b.queue = make([]*model.APILog, 0, recordFlushThreshold)
		b.mu.Unlock()
		go b.write(batch)
		return
	}
	b.mu.Unlock()
}

func (b *logBuffer) startFlusher() {
	go func() {
		t := time.NewTicker(recordFlushInterval)
		defer t.Stop()
		for range t.C {
			b.mu.Lock()
			if len(b.queue) == 0 {
				b.mu.Unlock()
				continue
			}
			batch := b.queue
			b.queue = make([]*model.APILog, 0, recordFlushThreshold)
			b.mu.Unlock()
			b.write(batch)
		}
	}()
}

func (b *logBuffer) write(batch []*model.APILog) {
	if len(batch) == 0 {
		return
	}
	// One INSERT, many rows. Failures swallowed — logging must never
	// surface to the caller.
	_ = database.GetDB().CreateInBatches(batch, 200).Error
}

// ---- query / purge ----

// Query returns paginated rows newest-first.
type APILogQuery struct {
	Path      string
	Method    string
	TokenName string
	Status    int
	Page      int
	Size      int
}

func (s *APILogService) Query(q APILogQuery) ([]*model.APILog, int64, error) {
	db := database.GetDB().Model(&model.APILog{})
	if q.Path != "" {
		db = db.Where("path LIKE ?", "%"+q.Path+"%")
	}
	if q.Method != "" {
		db = db.Where("method = ?", q.Method)
	}
	if q.TokenName != "" {
		db = db.Where("token_name = ?", q.TokenName)
	}
	if q.Status != 0 {
		db = db.Where("status = ?", q.Status)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page := q.Page
	if page <= 0 {
		page = 1
	}
	size := q.Size
	if size <= 0 {
		size = 50
	}
	if size > 200 {
		size = 200
	}
	var items []*model.APILog
	if err := db.Order("at desc").
		Offset((page - 1) * size).Limit(size).
		Find(&items).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, 0, err
	}
	return items, total, nil
}

// Purge removes log rows older than apiLogGCAfter; called from cron.
// Done in bounded chunks so a giant backlog (e.g. after a long
// downtime) doesn't hold the sqlite write lock for minutes. Each
// chunk's DELETE WHERE id IN (...) is a fast indexed delete.
func (s *APILogService) Purge() (int64, error) {
	cutoff := time.Now().Add(-apiLogGCAfter).Unix()
	var totalAffected int64
	for {
		var ids []int
		err := database.GetDB().Model(&model.APILog{}).
			Where("at < ?", cutoff).
			Order("id asc").
			Limit(purgeChunkSize).
			Pluck("id", &ids).Error
		if err != nil {
			return totalAffected, err
		}
		if len(ids) == 0 {
			return totalAffected, nil
		}
		res := database.GetDB().Where("id IN ?", ids).Delete(&model.APILog{})
		if res.Error != nil {
			return totalAffected, res.Error
		}
		totalAffected += res.RowsAffected
		if len(ids) < purgeChunkSize {
			return totalAffected, nil
		}
	}
}
