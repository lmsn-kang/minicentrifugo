package config

type Config struct {
	Port      string `json:"port"`
	RedisAddr string `json:"redis_addr"`
	NatsURL   string `json:"nats_url"`
}

var C = Config{
	Port:      "8000",
	RedisAddr: "127.0.0.1:6379",
	NatsURL:   "nats://127.0.0.1:4222",
}
