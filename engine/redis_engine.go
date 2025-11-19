package engine

import (
	"context"
	"encoding/json"
	"time"

	"minicentrifugo/config"
	"minicentrifugo/type"
	
	"github.com/redis/go-redis/v9"
)

type RedisEngine struct{
	rdb	*redis.Client 
	ctx	context.Context
}
func NewRedisEngine() *RedisEngine{
	rdb :=redis.NewClient(&redis.Options{
		Addr: config.C.RedisAddr,
	})
	return &RedisEngine{
		rdb: rdb,
		ctx: context.Background(),
	}
}

func (e *RedisEngine) AddPresence(channel string, info type.ClientInfo, expireAt int64) {
    key := "presence:" + channel
    data, _ := json.Marshal(info)
    e.rdb.HSet(e.ctx, key, info.ClientID, data)
    e.rdb.ExpireAt(e.ctx, key, time.Unix(expireAt, 0))
}

func (e *RedisEngine) RemovePresence(channel, clientID string) {
    key := "presence:" + channel
    e.rdb.HDel(e.ctx, key, clientID)
} 


func (e *RedisEngine) AddHistory(channel string, msg []byte, limit int64) error {
    key := "history:" + channel
    

    err := e.rdb.XAdd(e.ctx, &redis.XAddArgs{
        Stream: key,
        MaxLen: limit,    
        Approx: true,     
        Values: map[string]interface{}{
            "data": msg,
        },
    }).Err()

    if err != nil {
        return err
    }

    // 给历史记录也加个过期时间（例如 2 天），防止冷频道占用内存
    e.rdb.Expire(e.ctx, key, 48*time.Hour)
    return nil
}


func (e *RedisEngine) History(channel string, limit int) ([][]byte, error) {
    key := "history:" + channel

    
    res, err := e.rdb.XRevRangeN(e.ctx, key, "+", "-", int64(limit)).Result()
    if err != nil {
        return nil, err
    }

    messages := make([][]byte, 0, len(res))
    
    for _, streamMsg := range res {
       
        if val, ok := streamMsg.Values["data"].(string); ok {
            messages = append(messages, []byte(val))
        }
    }
    return messages, nil
}

func (e *RedisEngine) Presence(channel string) ([]types.ClientInfo, error) {
    key := "presence:" + channel
    
   
    val, err := e.rdb.HGetAll(e.ctx, key).Result()
    if err != nil {
        return nil, err
    }

    
    clients := make([]types.ClientInfo, 0, len(val))

    for _, data := range val {
        var info types.ClientInfo
        
        if err := json.Unmarshal([]byte(data), &info); err == nil {
            clients = append(clients, info)
        }
    }
    return clients, nil
}