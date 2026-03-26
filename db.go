package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// Store wraps the SQLite database and provides game-data access methods.
type Store struct {
	db *sql.DB
}

// OpenDB opens (or creates) the SQLite database at path, applies the schema,
// and seeds default rows if the tables are empty.
func OpenDB(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	if _, err = db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		return nil, err
	}

	schema := `
	CREATE TABLE IF NOT EXISTS players (
		uuid                   TEXT PRIMARY KEY,
		alias                  TEXT NOT NULL,
		nanopods               INTEGER NOT NULL DEFAULT 120000,
		gems                   INTEGER NOT NULL DEFAULT 150000,
		lifetime_value_dollars REAL NOT NULL DEFAULT 100,
		leaderboard_tier       TEXT NOT NULL DEFAULT 'Tier0'
	);
	CREATE TABLE IF NOT EXISTS friends (
		uuid  TEXT PRIMARY KEY,
		alias TEXT NOT NULL,
		level INTEGER NOT NULL DEFAULT 1
	);
	CREATE TABLE IF NOT EXISTS virtual_fragments (
		doc_type  TEXT PRIMARY KEY,
		blob_json TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS document_fragments (
		key        TEXT PRIMARY KEY,
		blob_json  TEXT NOT NULL,
		hash       TEXT NOT NULL,
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE TABLE IF NOT EXISTS transactions (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		player_uuid    TEXT,
		tc_id          INTEGER NOT NULL,
		timeline_id    INTEGER NOT NULL,
		transaction_id INTEGER NOT NULL,
		facade         TEXT,
		method_name    TEXT,
		args           TEXT,
		created_at     TEXT NOT NULL DEFAULT (datetime('now'))
	);
	`
	if _, err = db.Exec(schema); err != nil {
		return nil, err
	}

	// Migrations — ADD COLUMN is idempotent when we ignore "duplicate column name".
	migrations := []string{
		`ALTER TABLE transactions ADD COLUMN player_uuid TEXT`,
		`ALTER TABLE players ADD COLUMN current_tc_id INTEGER`,
	}
	for _, m := range migrations {
		if _, err = db.Exec(m); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				return nil, err
			}
		}
	}

	s := &Store{db: db}
	if err = s.seed(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) seed() error {
	var count int

	// Seed players
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM players`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		_, err := s.db.Exec(
			`INSERT INTO players (uuid, alias, nanopods, gems, lifetime_value_dollars, leaderboard_tier) VALUES (?, ?, ?, ?, ?, ?)`,
			"8d0ed094-4f5c-417e-bd29-489ce818e570", "Tenshii", 120000, 150000, 100.0, "Tier0",
		)
		if err != nil {
			return err
		}
	}

	// Seed friends
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM friends`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		_, err := s.db.Exec(
			`INSERT INTO friends (uuid, alias, level) VALUES (?, ?, ?)`,
			"11111111-1111-1111-1111-111111111111", "Test Friend", 10,
		)
		if err != nil {
			return err
		}
	}

	// Seed virtual_fragments (only the catch-all VIRTUAL_PlayerFriends blob;
	// currency/info/tier are generated dynamically from the players row)
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM virtual_fragments`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		_, err := s.db.Exec(
			`INSERT INTO virtual_fragments (doc_type, blob_json) VALUES (?, ?)`,
			"VIRTUAL_PlayerFriends",
			`{"friendPlayerViews":{},"_t":"VirtualPlayerFriends:v1"}`,
		)
		if err != nil {
			return err
		}
	}

	// Seed minimal village fragments for each friend (INSERT OR IGNORE — never overwrite gameplay data).
	friendRows, err := s.db.Query(`SELECT uuid FROM friends`)
	if err != nil {
		return err
	}
	var friendUUIDs []string
	for friendRows.Next() {
		var uuid string
		if err := friendRows.Scan(&uuid); err != nil {
			friendRows.Close()
			return err
		}
		friendUUIDs = append(friendUUIDs, uuid)
	}
	friendRows.Close()
	if err := friendRows.Err(); err != nil {
		return err
	}
	for _, uuid := range friendUUIDs {
		if err := s.seedFriendVillage(uuid); err != nil {
			return err
		}
	}

	return nil
}

// seedFriendVillage inserts the minimal document fragments required to visit a
// friend's island without a crash.  Uses INSERT OR IGNORE so existing data is
// never overwritten.
func (s *Store) seedFriendVillage(uuid string) error {
	fragListBlob := `{"_t":"GameEntityListFragment:v1","entities":{"villageGrid":true}}`
	gridBlob := `{"_compositionName":"villageGrid_v16.map!villageGrid","_t":"GameEntityFragment:v1","villageGrid":{}}`
	cacheBlob := `{"_t":"DurableCacheFragment:v1","caches":{"villageFrequentComponentTypeToEntityIds":{"MillDurableComponent":[],"OrderBuildingDurableComponent":[],"StorageDurableComponent":[]},"villageGridEntityLookupCache":{"village":"villageGrid"}}}`
	// These GamePlayer fragments must exist for the friend so that when the
	// client visits their island it can load them instead of trying to create
	// them cross-player (which is not allowed without a special flag).
	diveSiteManagementBlob := `{"_t":"TKDiveSiteManagementFragment:v1","lastDespawnTimeMillis":0,"rewardSeed":1000000,"inactiveSiteFragmentIds":[],"completedPromoDives":{},"seals":[],"socialRewardSeed":1000000}`
	storageBlob := `{"_t":"StorageFragment:v1","storage":[]}`
	badgesBlob := `{"_t":"BadgeFragment:v1","badgeStates":{}}`
	regionsBlob := `{"_t":"RegionFragment:v1","unlockedRegions":[]}`
	// EntityCollection:D[GamePlayer] — the GamePlayer entity holds the wallet
	// component. Without it, GamePlayerFacade.getWalletComponent fails trying
	// to createIfNeeded outside a transaction.
	gpEntityListBlob := `{"_t":"GameEntityListFragment:v1","entities":{"gamePlayer":true}}`
	gpEntityBlob := `{"_compositionName":"player","_t":"GameEntityFragment:v1","facebookIncentive":{"hasReceivedIncentiveRewards":false},"level":[],"preferences":[],"wallet":{"capacity":[],"receivedInitialResources":true,"wealth":{}}}`

	entries := []struct {
		key  string
		blob string
	}{
		{"EntityCollection:FragmentList:D[Village]P[" + uuid + "]", fragListBlob},
		{"EntityCollection:villageGrid:D[Village]P[" + uuid + "]", gridBlob},
		{"DurableCache:default:P[" + uuid + "]", cacheBlob},
		{"GamePlayer:TKDiveSiteManagementFragment:P[" + uuid + "]", diveSiteManagementBlob},
		{"GamePlayer:storage:P[" + uuid + "]", storageBlob},
		{"GamePlayer:badges:P[" + uuid + "]", badgesBlob},
		{"GamePlayer:Regions:P[" + uuid + "]", regionsBlob},
		{"EntityCollection:FragmentList:D[GamePlayer]P[" + uuid + "]", gpEntityListBlob},
		{"EntityCollection:gamePlayer:D[GamePlayer]P[" + uuid + "]", gpEntityBlob},
	}

	for _, e := range entries {
		sum := sha256.Sum256([]byte(e.blob))
		hash := fmt.Sprintf("%x", sum)
		_, err := s.db.Exec(
			`INSERT OR IGNORE INTO document_fragments (key, blob_json, hash, updated_at) VALUES (?, ?, ?, datetime('now'))`,
			e.key, e.blob, hash,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// PlayerRow holds the data for the single player account.
type PlayerRow struct {
	UUID                 string
	Alias                string
	Nanopods             int64
	Gems                 int64
	LifetimeValueDollars float64
	LeaderboardTier      string
}

// GetPlayer returns the first (and typically only) player row.
func (s *Store) GetPlayer() (PlayerRow, error) {
	var p PlayerRow
	err := s.db.QueryRow(
		`SELECT uuid, alias, nanopods, gems, lifetime_value_dollars, leaderboard_tier FROM players LIMIT 1`,
	).Scan(&p.UUID, &p.Alias, &p.Nanopods, &p.Gems, &p.LifetimeValueDollars, &p.LeaderboardTier)
	return p, err
}

// GetFriends returns all rows from the friends table.
func (s *Store) GetFriends() ([]Friend, error) {
	rows, err := s.db.Query(`SELECT uuid, alias, level FROM friends`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var friends []Friend
	for rows.Next() {
		var f Friend
		if err := rows.Scan(&f.UUID, &f.Alias, &f.Level); err != nil {
			return nil, err
		}
		friends = append(friends, f)
	}
	return friends, rows.Err()
}

// GetVirtualFragment returns the stored blob_json for the given doc_type.
// Returns sql.ErrNoRows if not found.
func (s *Store) GetVirtualFragment(docType string) (string, error) {
	var blob string
	err := s.db.QueryRow(`SELECT blob_json FROM virtual_fragments WHERE doc_type = ?`, docType).Scan(&blob)
	return blob, err
}

// SetPlayerTcID persists the client's current transaction context ID for the player.
func (s *Store) SetPlayerTcID(playerUUID string, tcID int) error {
	_, err := s.db.Exec(`UPDATE players SET current_tc_id = ? WHERE uuid = ?`, tcID, playerUUID)
	return err
}

// GetPlayerTcID returns the last known transaction context ID for the player, or 0 if unset.
func (s *Store) GetPlayerTcID(playerUUID string) int {
	var tcID int
	s.db.QueryRow(`SELECT COALESCE(current_tc_id, 0) FROM players WHERE uuid = ?`, playerUUID).Scan(&tcID)
	return tcID
}

// RecordTransaction inserts a row into the transactions audit log.
func (s *Store) RecordTransaction(playerUUID string, tcID, timelineID, transactionID int, facade, methodName, args string) error {
	_, err := s.db.Exec(
		`INSERT INTO transactions (player_uuid, tc_id, timeline_id, transaction_id, facade, method_name, args) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		playerUUID, tcID, timelineID, transactionID, facade, methodName, args,
	)
	return err
}

// UpsertFragment stores (or replaces) a document fragment by its full key.
func (s *Store) UpsertFragment(key, blobJSON, hash string) error {
	_, err := s.db.Exec(
		`INSERT INTO document_fragments (key, blob_json, hash, updated_at) VALUES (?, ?, ?, datetime('now'))
		 ON CONFLICT(key) DO UPDATE SET blob_json=excluded.blob_json, hash=excluded.hash, updated_at=excluded.updated_at`,
		key, blobJSON, hash,
	)
	return err
}

// DeleteFragment removes a document fragment by key.
func (s *Store) DeleteFragment(key string) error {
	_, err := s.db.Exec(`DELETE FROM document_fragments WHERE key = ?`, key)
	return err
}

// DeleteFragmentsForDocument removes all stored fragments whose key matches
// "{docType}:*:{documentID}". Used to clear stale fragments before re-inserting
// a fresh consistent set (e.g. from startPlaySession).
func (s *Store) DeleteFragmentsForDocument(docType, documentID string) error {
	_, err := s.db.Exec(
		`DELETE FROM document_fragments WHERE key LIKE ? AND key LIKE ?`,
		docType+":%", "%:"+documentID,
	)
	return err
}

// GetFragment returns the stored blob_json for a full fragment key.
// Returns sql.ErrNoRows if not found.
func (s *Store) GetFragment(key string) (string, error) {
	var blob string
	err := s.db.QueryRow(`SELECT blob_json FROM document_fragments WHERE key = ?`, key).Scan(&blob)
	return blob, err
}

// GetFragmentsForDocument returns all stored fragments matching a 2-part requestDocuments
// key of the form "{docType}:{documentId}" by querying for keys of the form
// "{docType}:*:{documentId}". Returns a map of full fragment key → blob_json.
//
// For EntityCollection documents, fragments are filtered through the stored FragmentList
// so that stale entity fragments from previous sessions are not served to the client.
func (s *Store) GetFragmentsForDocument(requestKey string) (map[string]string, error) {
	sep := strings.Index(requestKey, ":")
	if sep == -1 {
		return nil, nil
	}
	docType := requestKey[:sep]
	documentID := requestKey[sep+1:]

	rows, err := s.db.Query(
		`SELECT key, blob_json FROM document_fragments WHERE key LIKE ? AND key LIKE ?`,
		docType+":%", "%:"+documentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := map[string]string{}
	for rows.Next() {
		var k, blob string
		if err := rows.Scan(&k, &blob); err != nil {
			return nil, err
		}
		result[k] = blob
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if docType == "EntityCollection" {
		result = filterByEntityList(result, documentID)
	}

	return result, nil
}

// filterByEntityList drops entity fragments not listed in the EntityCollection FragmentList.
// This prevents stale entities from old sessions being served when the authoritative
// FragmentList has since shrunk (e.g. an entity was deleted and re-created with a new ID).
func filterByEntityList(frags map[string]string, documentID string) map[string]string {
	fragListKey := "EntityCollection:FragmentList:" + documentID
	fragListBlob, ok := frags[fragListKey]
	if !ok {
		return frags // no FragmentList yet — can't filter safely
	}

	var fragList struct {
		Entities map[string]bool `json:"entities"`
	}
	if err := json.Unmarshal([]byte(fragListBlob), &fragList); err != nil || fragList.Entities == nil {
		return frags // unparseable — return unfiltered
	}

	prefix := "EntityCollection:"
	suffix := ":" + documentID
	filtered := make(map[string]string, len(frags))
	for key, blob := range frags {
		if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, suffix) {
			filtered[key] = blob
			continue
		}
		entityID := key[len(prefix) : len(key)-len(suffix)]
		// Always keep the FragmentList itself and any entity the list references.
		if entityID == "FragmentList" || fragList.Entities[entityID] {
			filtered[key] = blob
		}
	}
	return filtered
}

// FragmentBlob resolves a fragmentBlobStoreKey to its JSON blob string.
// Key format: "{documentType}:{fragmentId}:{documentId}"
func (s *Store) FragmentBlob(fragmentBlobStoreKey string) string {
	// Check the document_fragments table first (populated from executeTransaction blobs).
	if blob, err := s.GetFragment(fragmentBlobStoreKey); err == nil {
		return blob
	}

	first := strings.Index(fragmentBlobStoreKey, ":")
	if first == -1 {
		return ""
	}
	docType := fragmentBlobStoreKey[:first]

	if docType == "FriendsInfo" {
		pStart := strings.Index(fragmentBlobStoreKey, "P[")
		pEnd := strings.LastIndex(fragmentBlobStoreKey, "]")
		if pStart == -1 || pEnd <= pStart {
			return ""
		}
		uuid := fragmentBlobStoreKey[pStart+2 : pEnd]
		friends, err := s.GetFriends()
		if err != nil {
			return ""
		}
		for _, f := range friends {
			if f.UUID == uuid {
				return friendsInfoBlob(f.Alias, f.Level)
			}
		}
		return ""
	}

	// For player-derived VIRTUAL types, build the blob dynamically from DB.
	p, err := s.GetPlayer()
	if err != nil {
		return ""
	}
	switch docType {
	case "VIRTUAL_PlayerCurrency":
		return playerCurrencyBlob(p.Nanopods, p.Gems, p.LifetimeValueDollars)
	case "VIRTUAL_PlayerInfo":
		return playerInfoBlob(p.Alias)
	case "VIRTUAL_LeaderboardTier":
		return leaderboardTierBlob(p.LeaderboardTier)
	}

	// Fall back to virtual_fragments table.
	blob, err := s.GetVirtualFragment(docType)
	if err != nil {
		return ""
	}
	return blob
}

func playerCurrencyBlob(nanopods, gems int64, lifetimeValue float64) string {
	b, _ := json.Marshal(map[string]interface{}{
		"_t": "VirtualPlayerCurrency:v1",
		"currencyBalances": map[string]interface{}{
			"Nanopods2": nanopods,
			"Gems":      gems,
		},
		"lifetimeValueDollars": lifetimeValue,
	})
	return string(b)
}

func playerInfoBlob(alias string) string {
	b, _ := json.Marshal(map[string]interface{}{
		"_t":        "VirtualPlayerInfo:v1",
		"bestAlias": alias,
	})
	return string(b)
}

func leaderboardTierBlob(tier string) string {
	b, _ := json.Marshal(map[string]interface{}{
		"_t":   "LeaderboardTierVirtualFragment:v1",
		"tier": tier,
	})
	return string(b)
}
