package type

type PublishMessage struct {
    Channel string `json:"channel"`
    Data    []byte `json:"data"`
}

