package main

import (
	"bytes"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"reflect"
)

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

	zlibReader, err := zlib.NewReader(conn)
	if err != nil {
		log.Println("Failed to create zlib reader:", err)
		return
	}
	defer zlibReader.Close()
	decoder := json.NewDecoder(zlibReader)

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
		case Connect, Reconnect:
			files := map[string]string{}
			if fd, ok := msgData.(map[string]interface{}); ok {
				if f2s, ok := fd["fileToSha1"].(map[string]interface{}); ok {
					for name, v := range f2s {
						files[name] = v.(string)
					}
				}
			}
			sendErr = sendMessage(conn, &nextRes, Connect, req, map[string]interface{}{
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
			})
			if sendErr == nil {
				sendErr = sendLuaMessage(conn, &nextRes, map[string]interface{}{
					"type":                           "sessionConfiguration",
					"sendClientBlobsWithTransaction": true,
				})
			}

		case Heartbeat:
			sendErr = sendMessage(conn, &nextRes, Heartbeat, req, nil)

		case OnNewTransactionContext:
			eventData := msgData.(map[string]interface{})
			transactionId := int(eventData["tcId"].(float64))
			timelineId := int(eventData["timelineId"].(float64))
			transactions[transactionId] = &TransactionContext{
				TransactionID: transactionId,
				TimelineID:    timelineId,
			}
			sendErr = sendMessage(conn, &nextRes, Luas, req, map[string]interface{}{})

		case RequestDocuments:
			message := msgData.(map[string]interface{})["message"].(map[string]interface{})
			transactionId := int(message["tcId"].(float64))
			if transactions[transactionId] == nil {
				log.Println("No transaction found:", transactionId)
				continue
			}

			blobStoreKeys := message["blobStoreKeys"].([]interface{})
			updates := make([]map[string]interface{}, 0, len(blobStoreKeys))
			for _, key := range blobStoreKeys {
				j, _ := json.Marshal(map[string]interface{}{})
				updates = append(updates, map[string]interface{}{
					"documentFragmentId": key.(string),
					"blobJson":           string(j),
				})
			}

			// Push data first, then ack
			sendErr = sendLuaMessage(conn, &nextRes, map[string]interface{}{
				"type":          "requestDocumentsResponse",
				"tcId":          transactionId,
				"blobStoreKeys": blobStoreKeys,
				"updates":       updates,
			})
			if sendErr == nil {
				sendErr = sendMessage(conn, &nextRes, Luas, req, map[string]interface{}{})
			}

		case RequestDocumentFragments:
			message := msgData.(map[string]interface{})["message"].(map[string]interface{})
			transactionId := int(message["tcId"].(float64))
			if transactions[transactionId] == nil {
				log.Println("No transaction found:", transactionId)
				continue
			}

			fragmentIds := message["documentFragmentIds"].([]interface{})
			updates := make([]map[string]interface{}, 0, len(fragmentIds))
			for _, id := range fragmentIds {
				j, _ := json.Marshal(map[string]interface{}{})
				updates = append(updates, map[string]interface{}{
					"documentFragmentId": id.(string),
					"blobJson":           string(j),
				})
			}

			// Push data first, then ack
			sendErr = sendLuaMessage(conn, &nextRes, map[string]interface{}{
				"type":          "requestDocumentsResponse",
				"tcId":          transactionId,
				"blobStoreKeys": fragmentIds,
				"updates":       updates,
			})
			if sendErr == nil {
				sendErr = sendMessage(conn, &nextRes, Luas, req, map[string]interface{}{})
			}

		case GetFriendPlayers:
			sendErr = sendMessage(conn, &nextRes, GetFriendPlayers, req, map[string]interface{}{
				"friends": []interface{}{},
				"more":    false,
			})

		case ValidateOnDemandFiles:
			files := map[string]string{}
			if fd, ok := msgData.(map[string]interface{}); ok {
				if f2s, ok := fd["fileToSha1"].(map[string]interface{}); ok {
					for name := range f2s {
						files[name] = ""
					}
				}
			}
			sendErr = sendMessage(conn, &nextRes, ValidateOnDemandFiles, req, files)

		case GetInAppProducts:
			sendErr = sendMessage(conn, &nextRes, cmd, req, map[string]interface{}{
				"products":            []interface{}{},
				"productsWithBundles": []interface{}{},
			})

		default:
			sendErr = sendMessage(conn, &nextRes, cmd, req, nil)
		}

		if sendErr != nil {
			log.Println("Failed to send message:", sendErr)
		}
	}
}

func sendMessage(conn net.Conn, nextRes *int, cmd Command, req float64, data any) error {
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
	return writeFrame(conn, jsonBytes)
}

func sendLuaMessage(conn net.Conn, nextRes *int, data any) error {
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
	return writeFrame(conn, jsonBytes)
}

func writeFrame(conn net.Conn, jsonBytes []byte) error {
	var zlibBuf bytes.Buffer
	zlibWriter := zlib.NewWriter(&zlibBuf)
	if _, err := zlibWriter.Write([]byte(fmt.Sprintf("%07d", len(jsonBytes)))); err != nil {
		return fmt.Errorf("failed to write length prefix: %w", err)
	}
	if _, err := zlibWriter.Write(jsonBytes); err != nil {
		return fmt.Errorf("failed to write payload: %w", err)
	}
	if err := zlibWriter.Close(); err != nil {
		return fmt.Errorf("failed to close zlib: %w", err)
	}
	_, err := conn.Write(zlibBuf.Bytes())
	return err
}
