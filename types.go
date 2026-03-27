package main

import "github.com/uptrace/bun"

type Command string

type Friend struct {
	bun.BaseModel `bun:"table:friends"`
	UUID          string `bun:"uuid,pk"`
	Alias         string `bun:"alias,notnull"`
	Level         int    `bun:"level,notnull,default:1"`
}

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
