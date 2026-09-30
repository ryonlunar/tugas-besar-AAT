package jetstreamconsumer

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func duration(name, fallback string) (time.Duration, error) {
	value, err := time.ParseDuration(env(name, fallback))
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return value, nil
}

func positiveInt(name, fallback string) (int, error) {
	value, err := strconv.Atoi(env(name, fallback))
	if err != nil || value < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

func ConfigFromEnv(durableVariable, defaultDurable string) (Config, error) {
	c := Config{URL: env("NATS_URL", "nats://nats:4222"), Token: os.Getenv("NATS_TOKEN"), Stream: env("NATS_STREAM_NAME", "HAZARD_EVENTS"), Durable: env(durableVariable, defaultDurable)}
	if c.Token == "" {
		return Config{}, fmt.Errorf("NATS_TOKEN is required")
	}
	var err error
	if c.AckWait, err = duration("NATS_ACK_WAIT", "30s"); err != nil {
		return Config{}, err
	}
	if c.FetchExpires, err = duration("NATS_FETCH_EXPIRES", "2s"); err != nil {
		return Config{}, err
	}
	if c.NAKDelay, err = duration("CONSUMER_NAK_DELAY", "5s"); err != nil {
		return Config{}, err
	}
	if c.MaxAckPending, err = positiveInt("NATS_MAX_ACK_PENDING", "64"); err != nil {
		return Config{}, err
	}
	c.MaxDeliver, err = strconv.Atoi(env("NATS_MAX_DELIVER", "-1"))
	if err != nil || c.MaxDeliver == 0 || c.MaxDeliver < -1 {
		return Config{}, fmt.Errorf("NATS_MAX_DELIVER must be -1 or a positive integer")
	}
	if c.FetchBatch, err = positiveInt("NATS_FETCH_BATCH", "10"); err != nil {
		return Config{}, err
	}
	if c.FetchBatch > c.MaxAckPending {
		return Config{}, fmt.Errorf("NATS_FETCH_BATCH exceeds NATS_MAX_ACK_PENDING")
	}
	return c, nil
}
