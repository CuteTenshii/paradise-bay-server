package main

import (
	"compress/zlib"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"reflect"
	"time"

	"github.com/google/uuid"
)

// traceEnabled turns on per-message socket logging (incoming cmd/req/ack and
// outgoing cmd/res) when PB_TRACE=1. Off by default to keep the log readable;
// enable it to diagnose connection drops.
var traceEnabled = os.Getenv("PB_TRACE") == "1"

// friendsInfoBlob returns a serialised FriendsInfoFragment for the given alias/level.
// The structure mirrors FriendsInfoFragment.init() → {info: getDefaultFriendsInfoView()}.
func friendsInfoBlob(alias string, level int) string {
	blob, _ := json.Marshal(map[string]interface{}{
		"_t": "FriendsInfoFragment:v1",
		"info": map[string]interface{}{
			"level":                    level,
			"diveExpiryTimeMillis":     0,
			"diveSiteExpiryTimeMillis": 0,
			"tradefestProgress":        map[string]interface{}{},
			"iconUri":                  "",
			"bestAlias":                alias,
			"socialFeedablePetInfo":    map[string]interface{}{},
		},
	})
	return string(blob)
}

func StartSocket(port int, store *Store) {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		panic(err)
	}
	defer listener.Close()
	fmt.Printf("✓ Socket listening on 127.0.0.1:%d\n\nAll good! Launch Paradise Bay from the Start menu and enjoy your island. 🏝️\nThis window can be minimized, keep it running in the background while you play.\n\n", port)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Fatal(err)
		}

		go handleMessage(conn, store)
	}
}

func handleMessage(conn net.Conn, store *Store) {
	defer conn.Close()

	// Per-connection state: res counter, transaction map, and the authenticated player UUID.
	nextRes := 0
	transactions := make(map[int]*TransactionContext)
	var playerUUID string

	// One continuous zlib stream for reading (client sends all messages in one stream)
	zlibReader, err := zlib.NewReader(conn)
	if err != nil {
		log.Println("Failed to create zlib reader:", err)
		return
	}
	defer zlibReader.Close()
	decoder := json.NewDecoder(zlibReader)

	// One continuous zlib stream for writing — MUST NOT close until connection ends.
	// Each message is flushed with Z_SYNC_FLUSH so the client can decompress
	// immediately without waiting for the stream to be finalised.
	zlibWriter := zlib.NewWriter(conn)
	defer zlibWriter.Close()

	for {
		var data any
		if err = decoder.Decode(&data); err != nil {
			if err == io.EOF {
				break
			}
			log.Println("Error decoding JSON:", err)
			break
		}
		if reflect.TypeOf(data).Kind() == reflect.Float64 {
			continue
		}

		mapData, ok := data.(map[string]interface{})
		if !ok {
			log.Println("Unexpected type:", reflect.TypeOf(data))
			continue
		}

		cmd := Command(mapData["cmd"].(string))
		var req float64
		if r, ok := mapData["req"]; ok && r != nil {
			req = r.(float64)
		}
		msgData := mapData["data"]

		if traceEnabled {
			ack := "-"
			if a, ok := mapData["ack"]; ok && a != nil {
				ack = fmt.Sprintf("%v", a)
			}
			log.Printf("<- cmd=%s req=%.0f ack=%s", cmd, req, ack)
		}

		if cmd == Connect {
			nextRes = 0
		}

		var sendErr error
		switch cmd {
		case Connect:
			player, playerErr := resolvePlayer(store, msgData)
			if playerErr != nil {
				log.Println("connect: failed to resolve player:", playerErr)
				break
			}
			playerUUID = player.UUID
			files := extractFileToSha1(msgData)
			sendErr = sendMessage(zlibWriter, &nextRes, Connect, req, connectPayload(player, files))
			// sessionConfiguration is sent in OnNewTransactionContext once we have the tcId.

		case Reconnect:
			// Resume the res sequence from where the client left off.
			// The client sends its last acknowledged res as "ack"; the next
			// message the client expects has res = ack + 1.
			if ackVal, ok := mapData["ack"]; ok && ackVal != nil {
				nextRes = int(ackVal.(float64)) + 1
			}
			player, playerErr := resolvePlayer(store, msgData)
			if playerErr != nil {
				log.Println("reconnect: failed to resolve player:", playerErr)
				break
			}
			playerUUID = player.UUID
			files := extractFileToSha1(msgData)
			sendErr = sendMessage(zlibWriter, &nextRes, Connect, req, connectPayload(player, files))
			// On reconnect, send sessionConfiguration immediately if we know the player's tcId
			// (e.g. server restarted between sessions). This sets sendClientBlobsWithTransaction
			// before any replayed transactions run.
			if sendErr == nil {
				if tcID := store.GetPlayerTcID(player.UUID); tcID != 0 {
					sendErr = sendLuaMessage(zlibWriter, &nextRes, map[string]interface{}{
						"type":                           "sessionConfiguration",
						"tcId":                           tcID,
						"sendClientBlobsWithTransaction": true,
					})
				}
			}

		case Heartbeat:
			sendErr = sendMessage(zlibWriter, &nextRes, Heartbeat, req, nil)

		case OnNewTransactionContext:
			eventData := msgData.(map[string]interface{})
			tcID := int(eventData["tcId"].(float64))
			timelineId := int(eventData["timelineId"].(float64))
			transactions[tcID] = &TransactionContext{
				TransactionID: tcID,
				TimelineID:    timelineId,
			}
			// Persist the tcId so reconnects can include it in sessionConfiguration.
			if playerUUID != "" {
				if err := store.SetPlayerTcID(playerUUID, tcID); err != nil {
					log.Printf("onNewTransactionContext: failed to save tcId: %v", err)
				}
			}
			sendErr = sendMessage(zlibWriter, &nextRes, Luas, req, map[string]interface{}{})
			// Send sessionConfiguration with the correct tcId so the client sets
			// sendClientBlobsWithTransaction=true before running startPlaySession.
			if sendErr == nil {
				sendErr = sendLuaMessage(zlibWriter, &nextRes, map[string]interface{}{
					"type":                           "sessionConfiguration",
					"tcId":                           tcID,
					"sendClientBlobsWithTransaction": true,
				})
			}

		case RequestDocuments:
			message := msgData.(map[string]interface{})["message"].(map[string]interface{})

			tcIdVal, hasTcId := message["tcId"]
			if !hasTcId || tcIdVal == nil {
				log.Println("requestDocuments: missing tcId, sending empty response")
				sendErr = sendLuaMessage(zlibWriter, &nextRes, map[string]interface{}{
					"type":          "requestDocumentsResponse",
					"tcId":          0,
					"blobStoreKeys": []interface{}{},
					"updates":       []interface{}{},
				})
				if sendErr == nil {
					sendErr = sendMessage(zlibWriter, &nextRes, Luas, req, map[string]interface{}{})
				}
				break
			}

			transactionId := int(tcIdVal.(float64))
			if transactions[transactionId] == nil {
				log.Printf("requestDocuments: auto-registering unknown tcId %d", transactionId)
				transactions[transactionId] = &TransactionContext{TransactionID: transactionId}
			}

			blobStoreKeys := message["blobStoreKeys"].([]interface{})

			updates := make([]map[string]interface{}, 0)
			for _, k := range blobStoreKeys {
				key, ok := k.(string)
				if !ok {
					continue
				}
				// Each requestDocuments key is a 2-part prefix "{docType}:{documentId}".
				// Look up all stored fragments matching "{docType}:*:{documentId}".
				frags, err := store.GetFragmentsForDocument(key)
				if err != nil {
					log.Printf("requestDocuments: error querying fragments for %s: %v", key, err)
					continue
				}
				for fragKey, blob := range frags {
					updates = append(updates, map[string]interface{}{
						"documentFragmentId": fragKey,
						"blobJson":           blob,
					})
				}
			}
			log.Printf("requestDocuments: returning %d fragments for %d keys", len(updates), len(blobStoreKeys))

			sendErr = sendLuaMessage(zlibWriter, &nextRes, map[string]interface{}{
				"type":          "requestDocumentsResponse",
				"tcId":          transactionId,
				"blobStoreKeys": blobStoreKeys,
				"updates":       updates,
			})
			if sendErr == nil {
				sendErr = sendMessage(zlibWriter, &nextRes, Luas, req, map[string]interface{}{})
			}

		case RequestDocumentFragments:
			message := msgData.(map[string]interface{})["message"].(map[string]interface{})

			tcIdVal, hasTcId := message["tcId"]
			if !hasTcId || tcIdVal == nil {
				log.Println("requestDocumentFragments: missing tcId")
				sendErr = sendMessage(zlibWriter, &nextRes, Luas, req, map[string]interface{}{})
				break
			}

			transactionId := int(tcIdVal.(float64))
			if transactions[transactionId] == nil {
				log.Printf("requestDocumentFragments: auto-registering unknown tcId %d", transactionId)
				transactions[transactionId] = &TransactionContext{TransactionID: transactionId}
			}

			fragmentIds := message["documentFragmentIds"].([]interface{})
			updates := make([]map[string]interface{}, 0, len(fragmentIds))
			for _, id := range fragmentIds {
				key := id.(string)
				blobJson := store.FragmentBlob(key)
				if blobJson == "" {
					log.Printf("requestDocumentFragments: no blob for %s, skipping", key)
					continue
				}
				updates = append(updates, map[string]interface{}{
					"documentFragmentId": key,
					"blobJson":           blobJson,
				})
			}

			// Push data first, then ack
			sendErr = sendLuaMessage(zlibWriter, &nextRes, map[string]interface{}{
				"type":          "requestDocumentsResponse",
				"tcId":          transactionId,
				"blobStoreKeys": fragmentIds,
				"updates":       updates,
			})
			if sendErr == nil {
				sendErr = sendMessage(zlibWriter, &nextRes, Luas, req, map[string]interface{}{})
			}

		case GetFriendPlayers:
			// Lua reads response.players: map of game-uuid → {friendType: platformId}.
			// We don't need real Facebook IDs — the game falls back to FriendsInfo blobs
			// for display name/level, so empty platform ID maps work fine.
			friends, friendsErr := store.GetFriends()
			if friendsErr != nil {
				log.Println("getFriendPlayers: failed to load friends:", friendsErr)
				friends = nil
			}
			players := make(map[string]interface{}, len(friends))
			for _, f := range friends {
				players[f.UUID] = map[string]interface{}{}
			}
			sendErr = sendMessage(zlibWriter, &nextRes, GetFriendPlayers, req, map[string]interface{}{
				"players": players,
				"more":    false,
			})

		case ExecuteTransaction:
			// The client sends a transaction for the server to validate and persist.
			// We accept every transaction optimistically: reflect back the same
			// transactionId/timelineId/tcId so the Lua transaction context marks it acked.
			// When sendClientBlobsWithTransaction=true, each verifyHashes entry includes a
			// .blob field with the actual JSON — we persist those to document_fragments.
			var transactionId, timelineId interface{}
			var tcId float64
			if md, ok := msgData.(map[string]interface{}); ok {
				if msg, ok := md["message"].(map[string]interface{}); ok {
					transactionId = msg["transactionId"]
					timelineId = msg["timelineId"]
					if v, ok := msg["tcId"].(float64); ok {
						tcId = v
					}

					// Parse invoke for the audit log.
					playerUUID, facade, methodName, args := "", "", "", ""
					if inv, ok := msg["invoke"].(map[string]interface{}); ok {
						methodName, _ = inv["methodName"].(string)
						if fc, ok := inv["facade"].(map[string]interface{}); ok {
							facade, _ = fc["c"].(string)
							if a, ok := fc["a"].([]interface{}); ok && len(a) > 0 {
								if ref, ok := a[0].(map[string]interface{}); ok {
									playerUUID, _ = ref["uuid"].(string)
								}
							}
						}
						if a, ok := inv["args"].([]interface{}); ok {
							if b, err := json.Marshal(a); err == nil {
								args = string(b)
							}
						}
					}
					tlID, tlOK := timelineId.(float64)
					txID, txOK := transactionId.(float64)
					if !tlOK || !txOK {
						log.Printf("executeTransaction: unexpected type for timelineId=%T transactionId=%T", timelineId, transactionId)
					} else {
						if err := store.RecordTransaction(
							playerUUID, int(tcId), int(tlID), int(txID),
							facade, methodName, args,
						); err != nil {
							log.Printf("executeTransaction: failed to record transaction: %v", err)
						} else {
							log.Printf("executeTransaction: recorded txn %d player=%s facade=%s method=%s", int(txID), playerUUID, facade, methodName)
						}
					}

					// Persist blobs from verifyHashes (present when sendClientBlobsWithTransaction=true).
					if vh, ok := msg["verifyHashes"].(map[string]interface{}); ok {
						for _, listKey := range []string{"insert", "update"} {
							if list, ok := vh[listKey].([]interface{}); ok {
								for _, entry := range list {
									e, ok := entry.(map[string]interface{})
									if !ok {
										continue
									}
									key, _ := e["key"].(string)
									hash, _ := e["value"].(string)
									blob, hasBlob := e["blob"].(string)
									if key == "" || hash == "" || !hasBlob {
										continue
									}
									if err := store.UpsertFragment(key, blob, hash); err != nil {
										log.Printf("executeTransaction: failed to upsert fragment %s: %v", key, err)
									} else {
										log.Printf("executeTransaction: stored fragment %s", key)
									}
								}
							}
						}
						if delList, ok := vh["delete"].([]interface{}); ok {
							for _, entry := range delList {
								if e, ok := entry.(map[string]interface{}); ok {
									if key, _ := e["key"].(string); key != "" {
										if err := store.DeleteFragment(key); err != nil {
											log.Printf("executeTransaction: failed to delete fragment %s: %v", key, err)
										}
									}
								}
							}
						}
					}
				}
			}
			sendErr = sendLuaMessage(zlibWriter, &nextRes, map[string]interface{}{
				"type":          "transactionAccepted",
				"tcId":          tcId,
				"transactionId": transactionId,
				"timelineId":    timelineId,
			})
			// Ack the "luas" request itself (matched by req) so the client clears the
			// pending request and cancels its ResponseTimeoutTimer. Without this the
			// socket channel times out (~20s) and force-reconnects, replaying
			// startPlaySession in a loop. The transactionAccepted above is an unsolicited
			// luaSessionMessage (no req) and cannot clear the pending request on its own.
			if sendErr == nil {
				sendErr = sendMessage(zlibWriter, &nextRes, Luas, req, map[string]interface{}{})
			}

		case ExecuteCommand:
			// serverOnly facade invocations — `.txn.serverOnly:method()` — arrive as
			// executeCommand rather than executeTransaction. The client blocks until we
			// reply with a `commandSucceeded` luaSessionMessage carrying the same
			// commandId and a JSON-encoded `result`; handle_commandSucceeded then fires
			// the onTransactionAccepted callback. Keani's "Choose Your Name" dialog
			// depends on this (getAliases → showUI, setGameAlias → submit); without a
			// handler it never opens. See issue #3.
			message, _ := msgData.(map[string]interface{})["message"].(map[string]interface{})
			if message == nil {
				log.Println("executeCommand: missing message payload")
				break
			}

			var commandID, tcID float64
			if v, ok := message["commandId"].(float64); ok {
				commandID = v
			}
			if v, ok := message["tcId"].(float64); ok {
				tcID = v
			}

			facade, methodName := "", ""
			var cmdArgs []interface{}
			if inv, ok := message["invoke"].(map[string]interface{}); ok {
				methodName, _ = inv["methodName"].(string)
				if fc, ok := inv["facade"].(map[string]interface{}); ok {
					facade, _ = fc["c"].(string)
				}
				cmdArgs, _ = inv["args"].([]interface{})
			}

			result := resolveServerCommand(store, playerUUID, methodName, cmdArgs)
			log.Printf("executeCommand: %s:%s -> result=%s", facade, methodName, result)
			sendErr = sendLuaMessage(zlibWriter, &nextRes, map[string]interface{}{
				"type":      "commandSucceeded",
				"commandId": commandID,
				"tcId":      tcID,
				"result":    result,
			})
			// Ack the "luas" request (matched by req) so the client cancels its
			// ResponseTimeoutTimer — same reason as executeTransaction above.
			if sendErr == nil {
				sendErr = sendMessage(zlibWriter, &nextRes, Luas, req, map[string]interface{}{})
			}

		case ValidateOnDemandFiles:
			// The client sends {fileToSha1: {filename: sha1, ...}} for files in
			// "verification needed" state (local SHA1 differs from manifest expected SHA1).
			// We respond with a flat {filename: url_or_empty} map where:
			//   ""      → file is valid, no download needed
			//   "<url>" → download URL for an updated file
			// For a private server all files are valid, so we return empty strings for all.
			files := map[string]string{}
			if fd, ok := msgData.(map[string]interface{}); ok {
				if f2s, ok := fd["fileToSha1"].(map[string]interface{}); ok {
					for name := range f2s {
						files[name] = ""
					}
				} else {
					log.Println("validateOnDemandFiles: missing or invalid fileToSha1 field")
				}
			} else {
				log.Println("validateOnDemandFiles: missing data payload")
			}
			log.Printf("validateOnDemandFiles: validating %d file(s), marking all up-to-date", len(files))
			sendErr = sendMessage(zlibWriter, &nextRes, ValidateOnDemandFiles, req, files)

		case GetInAppProducts:
			sendErr = sendMessage(zlibWriter, &nextRes, cmd, req, map[string]interface{}{
				"products":            []interface{}{},
				"productsWithBundles": []interface{}{},
			})

		default:
			sendErr = sendMessage(zlibWriter, &nextRes, cmd, req, nil)
		}

		if sendErr != nil {
			log.Println("Failed to send message:", sendErr)
		}
	}
}

// resolvePlayer looks up the player from a connect/reconnect message data payload.
//
// If the message contains a cid, that UUID is used for the lookup.
// If there is no cid (first launch), the device's z2did is used instead.
// A new player row is created automatically when neither matches an existing record.
func resolvePlayer(store *Store, msgData any) (Player, error) {
	cid, z2did := "", ""
	if fd, ok := msgData.(map[string]interface{}); ok {
		cid, _ = fd["cid"].(string)
		if udid, ok := fd["udidInfo"].(map[string]interface{}); ok {
			z2did, _ = udid["z2did"].(string)
		}
	}

	// Returning player: look up by cid.
	if cid != "" {
		p, err := store.GetPlayerByCID(cid)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Player{}, err
		}
		// cid not in DB — fall through to z2did / create.
	}

	// First launch or recovered device: look up by z2did.
	if z2did != "" {
		p, err := store.GetPlayerByZ2DID(z2did)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Player{}, err
		}
	}

	// Brand-new player: generate a UUID and create the row.
	newUUID := uuid.New().String()
	log.Printf("connect: new player %s (z2did=%s) — creating account", newUUID, z2did)
	return store.CreatePlayer(newUUID, "Guest", z2did)
}

// resolveServerCommand computes the JSON-string result for a serverOnly facade
// command (executeCommand). The client runs the returned value through
// decodeJsonWithSpecialTypeDeserialization, so it must be a JSON string — or the
// literal "null" to decode as nil. Unknown commands are acked with "null" so no
// serverOnly flow blocks forever waiting for a response.
func resolveServerCommand(store *Store, playerUUID, methodName string, args []interface{}) string {
	switch methodName {
	case "getAliases":
		alias, gameAlias := playerAliases(store, playerUUID)
		aliases := map[string]interface{}{"bestAlias": alias}
		// Send the stored name only if the player already picked one; otherwise leave
		// gameAlias out so the client shows an empty field for the player to type into.
		if gameAlias != "" {
			aliases["gameAlias"] = gameAlias
		}
		b, _ := json.Marshal(aliases)
		return string(b)

	case "setGameAlias":
		name := ""
		if len(args) > 0 {
			name, _ = args[0].(string)
		}
		if name == "" || playerUUID == "" {
			return "false"
		}
		if err := store.SetPlayerGameAlias(playerUUID, name); err != nil {
			log.Printf("executeCommand setGameAlias: failed to persist name %q: %v", name, err)
			return "false"
		}
		return "true"

	default:
		return "null"
	}
}

// playerAliases returns the connected player's login alias (bestAlias) and the
// name entered in Keani's "Choose Your Name" dialog (gameAlias), falling back to
// the first player row (and finally "Guest") so getAliases always resolves.
func playerAliases(store *Store, playerUUID string) (alias, gameAlias string) {
	var p Player
	var err error
	if playerUUID != "" {
		p, err = store.GetPlayerByCID(playerUUID)
	} else {
		err = sql.ErrNoRows
	}
	if err != nil {
		if p, err = store.GetPlayer(); err != nil {
			return "Guest", ""
		}
	}
	return p.Alias, p.GameAlias
}

// extractFileToSha1 pulls the fileToSha1 map out of a connect/reconnect data payload.
func extractFileToSha1(msgData any) map[string]string {
	files := map[string]string{}
	if fd, ok := msgData.(map[string]interface{}); ok {
		if f2s, ok := fd["fileToSha1"].(map[string]interface{}); ok {
			for name, v := range f2s {
				files[name] = v.(string)
			}
		}
	}
	return files
}

// connectPayload builds the data map for a connect/reconnect response.
func connectPayload(player Player, files map[string]string) map[string]interface{} {
	return map[string]interface{}{
		"urls":         []string{},
		"pushCmdPairs": []interface{}{},
		"cid":          player.UUID,
		"kid":          player.UUID,
		"loginResponse": map[string]interface{}{
			"uuid":             player.UUID,
			"requestedCid":     player.UUID,
			"bestAlias":        player.Alias,
			"currencyBalances": map[string]interface{}{},
		},
		"filesToOTA":          []interface{}{},
		"fileToSha1":          files,
		"zenSettings":         map[string]interface{}{},
		"connectResponseData": []any{},
		"sessionConfig": map[string]interface{}{
			"serverTimeMillis": time.Now().UnixMilli(),
		},
	}
}

func sendMessage(w *zlib.Writer, nextRes *int, cmd Command, req float64, data any) error {
	message := ClientMessage{
		Cmd:      cmd,
		Data:     data,
		Request:  req,
		Response: float64(*nextRes),
		Session:  "session",
	}
	if traceEnabled {
		log.Printf("-> cmd=%s req=%.0f res=%d", cmd, req, *nextRes)
	}
	*nextRes++
	jsonBytes, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	return writeFrame(w, jsonBytes)
}

func sendLuaMessage(w *zlib.Writer, nextRes *int, data any) error {
	message := LuaMessage{
		Cmd:      LuaSessionMessage,
		Response: float64(*nextRes),
		Data:     data,
	}
	if traceEnabled {
		luaType := ""
		if m, ok := data.(map[string]interface{}); ok {
			luaType, _ = m["type"].(string)
		}
		log.Printf("-> luaSessionMessage res=%d type=%s", *nextRes, luaType)
	}
	*nextRes++
	jsonBytes, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	return writeFrame(w, jsonBytes)
}

// writeFrame writes a length-prefixed JSON message into the shared zlib stream
// and flushes it so the client can decompress it immediately (Z_SYNC_FLUSH).
// It does NOT close the writer — the stream must stay open for the connection lifetime.
func writeFrame(w *zlib.Writer, jsonBytes []byte) error {
	if _, err := fmt.Fprintf(w, "%07d", len(jsonBytes)); err != nil {
		return fmt.Errorf("failed to write length prefix: %w", err)
	}
	if _, err := w.Write(jsonBytes); err != nil {
		return fmt.Errorf("failed to write payload: %w", err)
	}
	// Flush forces a Z_SYNC_FLUSH sync point so the data is immediately
	// decompressable on the client side without closing the stream.
	if err := w.Flush(); err != nil {
		return fmt.Errorf("failed to flush zlib: %w", err)
	}
	return nil
}
