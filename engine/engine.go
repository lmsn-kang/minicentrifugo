package engine

import (
	"minicentrifugo/type"
)


type Engine interface {
	
	AddPresence(channel string, info type.ClientInfo, expireAt int64)
	
	
	RemovePresence(channel, clientID string)
	
	AddHistory(channel string, msg []byte, limit int64) error

	History(channel string, limit int) ([][]byte, error)
	
	Presence(channel string) ([]types.ClientInfo, error)
}