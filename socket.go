package main

import (
	"compress/zlib"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"reflect"
	"time"
)

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
	fmt.Printf("Socket started on 127.0.0.1:%d\n", port)

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

	// Per-connection state: strictly incrementing res counter and transaction map
	nextRes := 0
	transactions := make(map[int]*TransactionContext)

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

		log.Printf("Received message: cmd=%s, req=%.0f, data=%v", cmd, req, msgData)

		if cmd == Connect {
			nextRes = 0
		}

		var sendErr error
		switch cmd {
		case Connect:
			player, playerErr := store.GetPlayer()
			if playerErr != nil {
				log.Println("connect: failed to load player:", playerErr)
				break
			}
			files := map[string]string{}
			if fd, ok := msgData.(map[string]interface{}); ok {
				if f2s, ok := fd["fileToSha1"].(map[string]interface{}); ok {
					for name, v := range f2s {
						files[name] = v.(string)
					}
				}
			}
			sendErr = sendMessage(zlibWriter, &nextRes, Connect, req, map[string]interface{}{
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
			})
			// sessionConfiguration is sent in OnNewTransactionContext once we have the tcId.

		case Reconnect:
			// Resume the res sequence from where the client left off.
			// The client sends its last acknowledged res as "ack"; the next
			// message the client expects has res = ack + 1.
			if ackVal, ok := mapData["ack"]; ok && ackVal != nil {
				nextRes = int(ackVal.(float64)) + 1
			}
			player, playerErr := store.GetPlayer()
			if playerErr != nil {
				log.Println("reconnect: failed to load player:", playerErr)
				break
			}
			files := map[string]string{}
			if fd, ok := msgData.(map[string]interface{}); ok {
				if f2s, ok := fd["fileToSha1"].(map[string]interface{}); ok {
					for name, v := range f2s {
						files[name] = v.(string)
					}
				}
			}
			sendErr = sendMessage(zlibWriter, &nextRes, Connect, req, map[string]interface{}{
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
			})
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
			player, playerErr := store.GetPlayer()
			if playerErr == nil {
				if err := store.SetPlayerTcID(player.UUID, tcID); err != nil {
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

func sendMessage(w *zlib.Writer, nextRes *int, cmd Command, req float64, data any) error {
	message := ClientMessage{
		Cmd:      cmd,
		Data:     data,
		Request:  req,
		Response: float64(*nextRes),
		Session:  "session",
	}
	*nextRes++
	jsonBytes, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	log.Println("[SENT]", string(jsonBytes))
	return writeFrame(w, jsonBytes)
}

func sendLuaMessage(w *zlib.Writer, nextRes *int, data any) error {
	message := LuaMessage{
		Cmd:      LuaSessionMessage,
		Response: float64(*nextRes),
		Data:     data,
	}
	*nextRes++
	jsonBytes, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	log.Println("[SENT]", string(jsonBytes))
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
