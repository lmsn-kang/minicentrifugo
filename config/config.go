package config

type Config struct {
	Port      string
	RedisAddr string
	NatsURL   string
}

var C = Config{
	Port:      "8000",
	RedisAddr: "127.0.0.1:6379",
	NatsURL:   "nats://127.0.0.1:4222",
}
