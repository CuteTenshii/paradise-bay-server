package main

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"reflect"
	"sync"
	"time"
)

type Command string

const (
	Connect                  Command = "connect"
	Reconnect                Command = "reconnect"
	Heartbeat                Command = "heartbeat"
	OnNewTransactionContext  Command = "onNewTransactionContext"
	TransactionAccepted      Command = "transactionAccepted"
	RequestDocumentFragments Command = "requestDocumentFragments"
	RequestDocumentsResponse Command = "requestDocumentsResponse"
	GetFriendPlayers         Command = "getFriendPlayers2"
	ValidateOnDemandFiles    Command = "validateOnDemandFiles"
)

type ClientMessage struct {
	Ack float64 `json:"ack"`
	// The command to execute
	Cmd Command `json:"cmd"`
	// Data of the message. Can be null
	Data    any     `json:"data"`
	Req     float64 `json:"req"`
	Service string  `json:"to"`
	Type    Command `json:"type"`
}

type ServerMessage struct {
	Ack float64 `json:"ack"`
	// The command to execute
	Cmd Command `json:"cmd"`
	// Data of the message. Can be null
	Data    any     `json:"data"`
	Req     float64 `json:"req"`
	Service string  `json:"to"`
}

type ConnectionState struct {
	nextReq float64
	mu      sync.Mutex
}

type TransactionContext struct {
	TransactionID int
	TimelineID    int
}

var state ConnectionState
var transactions = make(map[int]*TransactionContext)

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

	// Wrap the connection with zlib reader
	zlibReader, err := zlib.NewReader(conn)
	if err != nil {
		log.Println("Failed to create zlib reader:", err)
		return
	}
	defer zlibReader.Close()
	decoder := json.NewDecoder(zlibReader)

	for {
		var data any
		if err := decoder.Decode(&data); err != nil {
			if err == io.EOF {
				// Connection closed, exit gracefully
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

		event := ServerMessage{
			Ack:     mapData["ack"].(float64),
			Cmd:     Command(mapData["cmd"].(string)),
			Data:    mapData["data"],
			Req:     mapData["req"].(float64),
			Service: mapData["to"].(string),
		}

		log.Printf("Received message: cmd=%s, req=%1.0f, to=%s, data=%s", event.Cmd, state.nextReq, event.Service, event.Data)

		if event.Cmd == Connect {
			state.nextReq = 0

			files := map[string]string{}
			filesNames := event.Data.(map[string]interface{})["fileToSha1"].(map[string]interface{})
			for name := range filesNames {
				files[name] = filesNames[name].(string)
			}

			err = sendMessage(conn, Connect, map[string]interface{}{
				// List of URLs to open in the browser (why?)
				"urls": []string{},
				"pushCmdPairs": []struct {
					Name string `json:"name"`
					Data any    `json:"data"`
				}{},
				"bundleID": "king.com.ParadiseBay",
				"sessionConfig": map[string]interface{}{
					"adsUseProductionUnits": false,
					"serverTimeMillis":      time.Now().UnixMilli(),
					"serverTimeDelta":       0,
				},
				"cid": "8d0ed094-4f5c-417e-bd29-489ce818e570",
				"kid": "8d0ed094-4f5c-417e-bd29-489ce818e570",

				"allowsFastConnect": true,
				"loginResponse": map[string]interface{}{
					"uuid":             "8d0ed094-4f5c-417e-bd29-489ce818e570",
					"requestedCid":     "8d0ed094-4f5c-417e-bd29-489ce818e570",
					"bestAlias":        "Tenshii",
					"currencyBalances": map[string]interface{}{},
					"currencyEvent":    map[string]interface{}{},
					"promoList":        []interface{}{},
				},
				"promoList":  []interface{}{},
				"filesToOTA": []interface{}{},
				"fileToSha1": files,
			}, event.Service)
		} else if event.Cmd == Reconnect {
			err = sendMessage(conn, Reconnect, map[string]interface{}{
				//
			}, event.Service)
		} else if event.Cmd == Heartbeat {
			err = sendMessage(conn, Heartbeat, nil, event.Service)
		} else if event.Cmd == OnNewTransactionContext {
			// Append transaction
			transactionId := int(event.Data.(map[string]interface{})["tcId"].(float64))
			timelineId := int(event.Data.(map[string]interface{})["timelineId"].(float64))
			transactions[transactionId] = &TransactionContext{TransactionID: transactionId, TimelineID: timelineId}

			err = sendLuaMessage(conn, TransactionAccepted, TransactionAccepted, map[string]interface{}{
				"tcId":          transactionId,
				"transactionId": transactionId,
				"timelineId":    timelineId,
				"blobStoreDelta": map[string]interface{}{
					"insert": []any{},
					"update": []any{},
					"delete": []any{},
				},
			})
		} else if event.Cmd == RequestDocumentFragments {
			// TODO: make structs
			transactionId := int(event.Data.(map[string]interface{})["message"].(map[string]interface{})["tcId"].(float64))
			transaction := transactions[transactionId]
			timelineId := transaction.TimelineID

			err = sendLuaMessage(conn, TransactionAccepted, RequestDocumentsResponse, map[string]interface{}{
				"tcId":           transactionId,
				"transactionId":  transactionId,
				"timelineId":     timelineId,
				"blobStoreDelta": []any{},
				"updates":        []any{},
			})
		} else if event.Cmd == GetFriendPlayers {
			err = sendMessage(conn, GetFriendPlayers, map[string]interface{}{
				"players": []map[string]interface{}{
					{
						"friend": map[string]interface{}{
							"uuid":         "8d0ed095-4f5c-417e-bd29-489ce818e570",
							"metadata":     map[string]interface{}{},
							"gameCenterId": "",
							"googlePlayId": "",
							"facebookId":   "",
						},
					},
				},
			}, event.Service)
		} else if event.Cmd == ValidateOnDemandFiles {
			files := map[string]string{}
			filesNames := event.Data.(map[string]interface{})["fileToSha1"].(map[string]interface{})
			for name := range filesNames {
				files[name] = fmt.Sprintf("%x", sha1.Sum([]byte(name)))
			}

			err = sendMessage(conn, ValidateOnDemandFiles, map[string]interface{}{
				//"data":       []interface{}{},
				"fileToSha1": files,
			}, event.Service)
		} else {
			err = sendMessage(conn, event.Cmd, nil, event.Service)
		}
		if err != nil {
			log.Println("Failed to send connect message:", err)
		}

		state.nextReq++
	}
}

func sendMessage(conn net.Conn, cmd Command, data any, service string) error {
	message := ServerMessage{
		Ack:     0,
		Cmd:     cmd,
		Data:    data,
		Req:     state.nextReq,
		Service: service,
	}
	jsonBytes, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	var zlibBuf bytes.Buffer
	zlibWriter := zlib.NewWriter(&zlibBuf)

	if _, err := zlibWriter.Write([]byte(fmt.Sprintf("%07d", len(jsonBytes)))); err != nil {
		return fmt.Errorf("failed to write compressed payload: %w", err)
	}
	if _, err := zlibWriter.Write(jsonBytes); err != nil {
		return fmt.Errorf("failed to write compressed payload: %w", err)
	}
	if err := zlibWriter.Close(); err != nil {
		return fmt.Errorf("failed to close zlib: %w", err)
	}

	log.Println("[SENT]", string(jsonBytes))

	// Send ZLIB STREAM
	_, err = conn.Write(zlibBuf.Bytes())
	return err
}

func sendLuaMessage(conn net.Conn, cmd Command, actionType Command, data any) error {
	data.(map[string]interface{})["type"] = cmd
	message := ClientMessage{
		Ack:     0,
		Cmd:     cmd,
		Type:    actionType,
		Data:    map[string]interface{}{"message": data},
		Req:     state.nextReq,
		Service: "luas",
	}
	jsonBytes, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	var zlibBuf bytes.Buffer
	zlibWriter := zlib.NewWriter(&zlibBuf)

	if _, err = zlibWriter.Write([]byte(fmt.Sprintf("%07d", len(jsonBytes)))); err != nil {
		return fmt.Errorf("failed to write compressed payload: %w", err)
	}
	if _, err = zlibWriter.Write(jsonBytes); err != nil {
		return fmt.Errorf("failed to write compressed payload: %w", err)
	}
	if err = zlibWriter.Close(); err != nil {
		return fmt.Errorf("failed to close zlib: %w", err)
	}

	log.Println("[SENT]", string(jsonBytes))

	// Send ZLIB STREAM
	_, err = conn.Write(zlibBuf.Bytes())
	return err
}
