package main

import (
	"compress/zlib"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"reflect"
	"strings"
	"time"
)

// Offline fake blobs for VIRTUAL fragment types (mirrors the game's own initialFakes).
// Key = document type prefix of the fragment blob store key.
var virtualFragmentFakes = map[string]string{
	"VIRTUAL_PlayerCurrency":  `{"currencyBalances":{"Nanopods2":120000,"Gems":150000},"lifetimeValueDollars":100,"_t":"VirtualPlayerCurrency:v1"}`,
	"VIRTUAL_PlayerInfo":      `{"bestAlias":"Lieutenant Herta","_t":"VirtualPlayerInfo:v1"}`,
	"VIRTUAL_PlayerFriends":   `{"friendPlayerViews":{},"_t":"VirtualPlayerFriends:v1"}`,
	"VIRTUAL_LeaderboardTier": `{"tier":"Tier0","_t":"LeaderboardTierVirtualFragment:v1"}`,
}

func fakeFragmentBlob(fragmentBlobStoreKey string) string {
	if colonIdx := strings.Index(fragmentBlobStoreKey, ":"); colonIdx != -1 {
		if fake, ok := virtualFragmentFakes[fragmentBlobStoreKey[:colonIdx]]; ok {
			return fake
		}
	}
	return ""
}

type Command string

const (
	Connect                  Command = "connect"
	Reconnect                Command = "reconnect"
	Heartbeat                Command = "heartbeat"
	OnNewTransactionContext  Command = "onNewTransactionContext"
	TransactionAccepted      Command = "transactionAccepted"
	RequestDocuments         Command = "requestDocuments"
	RequestDocumentFragments Command = "requestDocumentFragments"
	RequestDocumentsResponse Command = "requestDocumentsResponse"
	GetFriendPlayers         Command = "getFriendPlayers2"
	ValidateOnDemandFiles    Command = "validateOnDemandFiles"
	LuaSessionMessage        Command = "luaSessionMessage"
	Luas                     Command = "luas"
	GetInAppProducts         Command = "getInAppProducts2"
	ExecuteTransaction       Command = "executeTransaction"
)

type ClientMessage struct {
	// The command to execute
	Cmd Command `json:"cmd"`
	// Data of the message. Can be nil
	Data any `json:"data"`
	// Request ID
	Request float64 `json:"req"`
	// Response ID
	Response float64 `json:"res"`
	// Session cookie
	Session string `json:"ses"`
}

type LuaMessage struct {
	Cmd      Command `json:"cmd"`
	Response float64 `json:"res"`
	Data     any     `json:"data"`
}

type TransactionContext struct {
	TransactionID int
	TimelineID    int
}

func StartSocket(port int) {
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

		go handleMessage(conn)
	}
}

func handleMessage(conn net.Conn) {
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
				"cid":          "8d0ed094-4f5c-417e-bd29-489ce818e570",
				"kid":          "8d0ed094-4f5c-417e-bd29-489ce818e570",
				"loginResponse": map[string]interface{}{
					"uuid":             "8d0ed094-4f5c-417e-bd29-489ce818e570",
					"requestedCid":     "8d0ed094-4f5c-417e-bd29-489ce818e570",
					"bestAlias":        "Tenshii",
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
			if sendErr == nil {
				sendErr = sendLuaMessage(zlibWriter, &nextRes, map[string]interface{}{
					"type":                           "sessionConfiguration",
					"sendClientBlobsWithTransaction": true,
				})
			}

		case Reconnect:
			// Resume the res sequence from where the client left off.
			// The client sends its last acknowledged res as "ack"; the next
			// message the client expects has res = ack + 1.
			if ackVal, ok := mapData["ack"]; ok && ackVal != nil {
				nextRes = int(ackVal.(float64)) + 1
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
				"cid":          "8d0ed094-4f5c-417e-bd29-489ce818e570",
				"kid":          "8d0ed094-4f5c-417e-bd29-489ce818e570",
				"loginResponse": map[string]interface{}{
					"uuid":             "8d0ed094-4f5c-417e-bd29-489ce818e570",
					"requestedCid":     "8d0ed094-4f5c-417e-bd29-489ce818e570",
					"bestAlias":        "Tenshii",
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
			if sendErr == nil {
				sendErr = sendLuaMessage(zlibWriter, &nextRes, map[string]interface{}{
					"type":                           "sessionConfiguration",
					"sendClientBlobsWithTransaction": true,
				})
			}

		case Heartbeat:
			sendErr = sendMessage(zlibWriter, &nextRes, Heartbeat, req, nil)

		case OnNewTransactionContext:
			eventData := msgData.(map[string]interface{})
			transactionId := int(eventData["tcId"].(float64))
			timelineId := int(eventData["timelineId"].(float64))
			transactions[transactionId] = &TransactionContext{
				TransactionID: transactionId,
				TimelineID:    timelineId,
			}
			sendErr = sendMessage(zlibWriter, &nextRes, Luas, req, map[string]interface{}{})

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
				log.Println("No transaction found:", transactionId)
				continue
			}

			blobStoreKeys := message["blobStoreKeys"].([]interface{})

			// Return empty updates — we don't have real document data and sending
			// malformed documentFragmentIds (missing the middle fragmentId segment)
			// causes client-side parse errors. The client tracks blobStoreKeys as
			// "requested" regardless of whether updates are present.
			sendErr = sendLuaMessage(zlibWriter, &nextRes, map[string]interface{}{
				"type":          "requestDocumentsResponse",
				"tcId":          transactionId,
				"blobStoreKeys": blobStoreKeys,
				"updates":       []interface{}{},
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
				log.Println("No transaction found:", transactionId)
				continue
			}

			fragmentIds := message["documentFragmentIds"].([]interface{})
			updates := make([]map[string]interface{}, 0, len(fragmentIds))
			for _, id := range fragmentIds {
				key := id.(string)
				blobJson := fakeFragmentBlob(key)
				if blobJson == "" {
					log.Printf("requestDocumentFragments: no fake blob for %s, skipping", key)
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
			// Lua reads response.players (map of uuid→{service→id}), not response.friends.
			sendErr = sendMessage(zlibWriter, &nextRes, GetFriendPlayers, req, map[string]interface{}{
				"players": map[string]interface{}{},
				"more":    false,
			})

		case ExecuteTransaction:
			// The client sends a transaction for the server to validate and persist.
			// We accept every transaction optimistically: reflect back the same
			// transactionId/timelineId/tcId so the Lua transaction context marks it acked.
			var transactionId, timelineId interface{}
			var tcId float64
			if md, ok := msgData.(map[string]interface{}); ok {
				if msg, ok := md["message"].(map[string]interface{}); ok {
					transactionId = msg["transactionId"]
					timelineId = msg["timelineId"]
					if v, ok := msg["tcId"].(float64); ok {
						tcId = v
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
