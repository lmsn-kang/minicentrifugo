package engine

import "cen-demo/type"

type Engine interface {
	AddPresence(channel string, info types.ClientInfo, expireAt int64)
	RemovePresence(channel, clientID string)
	Presence(channel string) ([]types.ClientInfo, error)
	AddHistory(channel string, msg []byte, limit int64) error
	History(channel string, limit int) ([][]byte, error)
}
