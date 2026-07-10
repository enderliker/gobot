package web

import (
	"database/sql"
	"log"
	"sync"
	"time"

	"gobot/internal/database"
)

type cachedStats struct {
	serverCount int
	lastUpdated time.Time
	mu          sync.RWMutex
}

var statsCache = &cachedStats{}

// GetLiveStats returns the cached server count or queries the database if cache expired
func GetLiveStats() int {
	statsCache.mu.RLock()
	if time.Since(statsCache.lastUpdated) < 5*time.Minute {
		count := statsCache.serverCount
		statsCache.mu.RUnlock()
		return count
	}
	statsCache.mu.RUnlock()

	statsCache.mu.Lock()
	defer statsCache.mu.Unlock()

	// Double check inside lock
	if time.Since(statsCache.lastUpdated) < 5*time.Minute {
		return statsCache.serverCount
	}

	count, err := queryServerCount()
	if err != nil {
		log.Printf("[WEB-STATS] Failed to query server count: %v", err)
		// Return previous cache value rather than failing
		return statsCache.serverCount
	}

	statsCache.serverCount = count
	statsCache.lastUpdated = time.Now()
	return count
}

func queryServerCount() (int, error) {
	if database.Default == nil {
		// Degrades gracefully if database connection is not initialized
		return 0, nil
	}

	// We use standard db querying. Since the database wrapper handles driver formats,
	// we query the db safely. Select count of active configured servers.
	// Filter out configs that don't have API keys set (if any).
	db := database.Default
	var count int
	// We format query parameters using DB helper if any parameters were present,
	// but this is a static query so no d.format() is strictly required.
	row := db.RawDB().QueryRow("SELECT COUNT(*) FROM guild_config WHERE api_key != ''")
	err := row.Scan(&count)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return count, err
}
