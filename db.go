package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	_ "modernc.org/sqlite"
)

// Store wraps the Bun database and provides game-data access methods.
type Store struct {
	db *bun.DB
}

// Player holds the data for the single player account.
type Player struct {
	bun.BaseModel        `bun:"table:players"`
	UUID                 string  `bun:"uuid,pk"`
	Alias                string  `bun:"alias,notnull"`
	Nanopods             int64   `bun:"nanopods,notnull,default:120000"`
	Gems                 int64   `bun:"gems,notnull,default:150000"`
	LifetimeValueDollars float64 `bun:"lifetime_value_dollars,notnull,default:100"`
	LeaderboardTier      string  `bun:"leaderboard_tier,notnull,default:'Tier0'"`
	CurrentTcID          *int    `bun:"current_tc_id"`
}

// VirtualFragment is a pre-computed game data blob keyed by doc_type.
type VirtualFragment struct {
	bun.BaseModel `bun:"table:virtual_fragments"`
	DocType       string `bun:"doc_type,pk"`
	BlobJSON      string `bun:"blob_json,notnull"`
}

// DocumentFragment is a persisted game entity fragment keyed by its full key.
type DocumentFragment struct {
	bun.BaseModel `bun:"table:document_fragments"`
	Key           string `bun:"key,pk"`
	BlobJSON      string `bun:"blob_json,notnull"`
	Hash          string `bun:"hash,notnull"`
	UpdatedAt     string `bun:"updated_at,notnull"`
}

// TransactionRecord is a row in the transactions audit log.
type TransactionRecord struct {
	bun.BaseModel `bun:"table:transactions"`
	ID            int64  `bun:"id,pk,autoincrement"`
	PlayerUUID    string `bun:"player_uuid"`
	TcID          int    `bun:"tc_id,notnull"`
	TimelineID    int    `bun:"timeline_id,notnull"`
	TransactionID int    `bun:"transaction_id,notnull"`
	Facade        string `bun:"facade"`
	MethodName    string `bun:"method_name"`
	Args          string `bun:"args"`
	CreatedAt     string `bun:"created_at,notnull"`
}

// OpenDB opens (or creates) the SQLite database at path, applies the schema,
// and seeds default rows if the tables are empty.
func OpenDB(path string) (*Store, error) {
	ctx := context.Background()

	sqldb, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	db := bun.NewDB(sqldb, sqlitedialect.New())

	if _, err = db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		return nil, err
	}

	// Create tables from models.
	models := []any{
		(*Player)(nil),
		(*Friend)(nil),
		(*VirtualFragment)(nil),
		(*DocumentFragment)(nil),
		(*TransactionRecord)(nil),
	}
	for _, model := range models {
		if _, err = db.NewCreateTable().Model(model).IfNotExists().Exec(ctx); err != nil {
			return nil, err
		}
	}

	// Migrations — ADD COLUMN is idempotent when we ignore "duplicate column name".
	migrations := []string{
		`ALTER TABLE transactions ADD COLUMN player_uuid TEXT`,
		`ALTER TABLE players ADD COLUMN current_tc_id INTEGER`,
	}
	for _, m := range migrations {
		if _, err = db.ExecContext(ctx, m); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				return nil, err
			}
		}
	}

	s := &Store{db: db}
	if err = s.seed(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) seed(ctx context.Context) error {
	// Seed players
	count, err := s.db.NewSelect().Model((*Player)(nil)).Count(ctx)
	if err != nil {
		return err
	}
	if count == 0 {
		p := &Player{
			UUID:                 "8d0ed094-4f5c-417e-bd29-489ce818e570",
			Alias:                "Tenshii",
			Nanopods:             120000,
			Gems:                 150000,
			LifetimeValueDollars: 100.0,
			LeaderboardTier:      "Tier0",
		}
		if _, err = s.db.NewInsert().Model(p).Exec(ctx); err != nil {
			return err
		}
	}

	// Seed friends
	count, err = s.db.NewSelect().Model((*Friend)(nil)).Count(ctx)
	if err != nil {
		return err
	}
	if count == 0 {
		f := &Friend{UUID: "11111111-1111-1111-1111-111111111111", Alias: "Test Friend", Level: 10}
		if _, err = s.db.NewInsert().Model(f).Exec(ctx); err != nil {
			return err
		}
	}

	// Seed virtual_fragments (only the catch-all VIRTUAL_PlayerFriends blob;
	// currency/info/tier are generated dynamically from the players row)
	count, err = s.db.NewSelect().Model((*VirtualFragment)(nil)).Count(ctx)
	if err != nil {
		return err
	}
	if count == 0 {
		vf := &VirtualFragment{
			DocType:  "VIRTUAL_PlayerFriends",
			BlobJSON: `{"friendPlayerViews":{},"_t":"VirtualPlayerFriends:v1"}`,
		}
		if _, err = s.db.NewInsert().Model(vf).Exec(ctx); err != nil {
			return err
		}
	}

	// Seed minimal village fragments for each friend (INSERT OR IGNORE — never overwrite gameplay data).
	var friends []Friend
	if err = s.db.NewSelect().Model(&friends).Column("uuid").Scan(ctx); err != nil {
		return err
	}
	for _, f := range friends {
		if err = s.seedFriendVillage(ctx, f.UUID); err != nil {
			return err
		}
	}

	return nil
}

// seedFriendVillage inserts the minimal document fragments required to visit a
// friend's island without a crash. Uses ON CONFLICT DO NOTHING so existing data is
// never overwritten.
func (s *Store) seedFriendVillage(ctx context.Context, uuid string) error {
	fragListBlob := `{"_t":"GameEntityListFragment:v1","entities":{"villageGrid":true}}`
	gridBlob := `{"_compositionName":"villageGrid_v16.map!villageGrid","_t":"GameEntityFragment:v1","villageGrid":{}}`
	cacheBlob := `{"_t":"DurableCacheFragment:v1","caches":{"villageFrequentComponentTypeToEntityIds":{"MillDurableComponent":[],"OrderBuildingDurableComponent":[],"StorageDurableComponent":[]},"villageGridEntityLookupCache":{"village":"villageGrid"}}}`
	diveSiteManagementBlob := `{"_t":"TKDiveSiteManagementFragment:v1","lastDespawnTimeMillis":0,"rewardSeed":1000000,"inactiveSiteFragmentIds":[],"completedPromoDives":{},"seals":[],"socialRewardSeed":1000000}`
	storageBlob := `{"_t":"StorageFragment:v1","storage":[]}`
	badgesBlob := `{"_t":"BadgeFragment:v1","badgeStates":{}}`
	regionsBlob := `{"_t":"RegionFragment:v1","unlockedRegions":[]}`
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

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	for _, e := range entries {
		sum := sha256.Sum256([]byte(e.blob))
		frag := &DocumentFragment{
			Key:       e.key,
			BlobJSON:  e.blob,
			Hash:      fmt.Sprintf("%x", sum),
			UpdatedAt: now,
		}
		if _, err := s.db.NewInsert().Model(frag).On("CONFLICT (key) DO NOTHING").Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

// GetPlayer returns the first (and typically only) player row.
func (s *Store) GetPlayer() (Player, error) {
	var p Player
	err := s.db.NewSelect().Model(&p).Limit(1).Scan(context.Background())
	return p, err
}

// GetFriends returns all rows from the friends table.
func (s *Store) GetFriends() ([]Friend, error) {
	var friends []Friend
	err := s.db.NewSelect().Model(&friends).Scan(context.Background())
	return friends, err
}

// GetVirtualFragment returns the stored blob_json for the given doc_type.
// Returns sql.ErrNoRows if not found.
func (s *Store) GetVirtualFragment(docType string) (string, error) {
	var vf VirtualFragment
	err := s.db.NewSelect().Model(&vf).Where("doc_type = ?", docType).Scan(context.Background())
	return vf.BlobJSON, err
}

// SetPlayerTcID persists the client's current transaction context ID for the player.
func (s *Store) SetPlayerTcID(playerUUID string, tcID int) error {
	_, err := s.db.NewUpdate().
		Model((*Player)(nil)).
		Set("current_tc_id = ?", tcID).
		Where("uuid = ?", playerUUID).
		Exec(context.Background())
	return err
}

// GetPlayerTcID returns the last known transaction context ID for the player, or 0 if unset.
func (s *Store) GetPlayerTcID(playerUUID string) int {
	var p Player
	s.db.NewSelect().Model(&p).Where("uuid = ?", playerUUID).Scan(context.Background()) //nolint:errcheck
	if p.CurrentTcID == nil {
		return 0
	}
	return *p.CurrentTcID
}

// RecordTransaction inserts a row into the transactions audit log.
func (s *Store) RecordTransaction(playerUUID string, tcID, timelineID, transactionID int, facade, methodName, args string) error {
	rec := &TransactionRecord{
		PlayerUUID:    playerUUID,
		TcID:          tcID,
		TimelineID:    timelineID,
		TransactionID: transactionID,
		Facade:        facade,
		MethodName:    methodName,
		Args:          args,
		CreatedAt:     time.Now().UTC().Format("2006-01-02 15:04:05"),
	}
	_, err := s.db.NewInsert().Model(rec).Exec(context.Background())
	return err
}

// UpsertFragment stores (or replaces) a document fragment by its full key.
func (s *Store) UpsertFragment(key, blobJSON, hash string) error {
	frag := &DocumentFragment{
		Key:       key,
		BlobJSON:  blobJSON,
		Hash:      hash,
		UpdatedAt: time.Now().UTC().Format("2006-01-02 15:04:05"),
	}
	_, err := s.db.NewInsert().Model(frag).
		On("CONFLICT (key) DO UPDATE SET blob_json = EXCLUDED.blob_json, hash = EXCLUDED.hash, updated_at = EXCLUDED.updated_at").
		Exec(context.Background())
	return err
}

// DeleteFragment removes a document fragment by key.
func (s *Store) DeleteFragment(key string) error {
	_, err := s.db.NewDelete().Model((*DocumentFragment)(nil)).Where("key = ?", key).Exec(context.Background())
	return err
}

// DeleteFragmentsForDocument removes all stored fragments whose key matches
// "{docType}:*:{documentID}". Used to clear stale fragments before re-inserting
// a fresh consistent set (e.g. from startPlaySession).
func (s *Store) DeleteFragmentsForDocument(docType, documentID string) error {
	_, err := s.db.NewDelete().Model((*DocumentFragment)(nil)).
		Where("key LIKE ?", docType+":%").
		Where("key LIKE ?", "%:"+documentID).
		Exec(context.Background())
	return err
}

// GetFragment returns the stored blob_json for a full fragment key.
// Returns sql.ErrNoRows if not found.
func (s *Store) GetFragment(key string) (string, error) {
	var frag DocumentFragment
	err := s.db.NewSelect().Model(&frag).Where("key = ?", key).Scan(context.Background())
	return frag.BlobJSON, err
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

	var frags []DocumentFragment
	err := s.db.NewSelect().Model(&frags).
		Where("key LIKE ?", docType+":%").
		Where("key LIKE ?", "%:"+documentID).
		Scan(context.Background())
	if err != nil {
		return nil, err
	}

	result := make(map[string]string, len(frags))
	for _, f := range frags {
		result[f.Key] = f.BlobJSON
	}

	if docType == "EntityCollection" {
		result = filterByEntityList(result, documentID)
	}

	return result, nil
}

// filterByEntityList drops entity fragments not listed in the EntityCollection FragmentList.
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
