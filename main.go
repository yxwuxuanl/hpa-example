package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/go-redis/redis/v8"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"log"
	"net/http"
	"os"
	"time"
)

var (
	redisHost      = flag.String("redis.host", "localhost:6379", "redis host")
	redisDb        = flag.Int("redis.db", 0, "redis database")
	serverAddr     = flag.String("server.addr", ":8080", "server address")
	redisQueueName = flag.String("redis.queue-name", "hpa-app", "")
)

const MetricNamespace = "hpa_app"

func main() {
	if len(os.Args) < 2 {
		printUsage()
	}

	var handler func(red *redis.Client)

	switch os.Args[1] {
	case "producer":
		handler = runProducer
	case "consumer":
		handler = runConsumer
	default:
		printUsage()
	}

	os.Args = os.Args[1:]
	flag.Parse()

	red := redis.NewClient(&redis.Options{
		Addr: *redisHost,
		DB:   *redisDb,
	})

	if err := red.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("failed to connect to redis: %v", err)
	}

	handler(red)

	http.Handle("GET /metrics", promhttp.Handler())

	log.Fatal(http.ListenAndServe(*serverAddr, nil))
}

func runProducer(red *redis.Client) {
	queueLengthGauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: MetricNamespace,
		Name:      "queue_length",
		ConstLabels: map[string]string{
			"queue_name": *redisQueueName,
		},
	})

	produceCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: MetricNamespace,
		Name:      "produce_count",
		ConstLabels: map[string]string{
			"queue_name": *redisQueueName,
		},
	})

	prometheus.MustRegister(queueLengthGauge, produceCounter)

	go func() {
		t := time.NewTicker(time.Second)
		for range t.C {
			queueLength, err := red.LLen(context.Background(), *redisQueueName).Result()
			if err != nil {
				log.Printf("Failed to get queue length: %v", err)
				continue
			}

			queueLengthGauge.Set(float64(queueLength))
		}
	}()

	http.HandleFunc("POST /{$}", func(rw http.ResponseWriter, r *http.Request) {
		cmd := red.LPush(
			r.Context(),
			*redisQueueName,
			time.Now().UnixNano(),
		)

		if err := cmd.Err(); err != nil {
			log.Printf("Failed to push to queue: %v", err)
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}

		produceCounter.Inc()

		v, _ := cmd.Result()
		fmt.Fprintf(rw, `{"queue_length":%d}`, v)
	})
}

func runConsumer(red *redis.Client) {
	consumeCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: MetricNamespace,
		Name:      "consume_count",
		ConstLabels: map[string]string{
			"queue_name": *redisQueueName,
		},
	})

	prometheus.MustRegister(consumeCounter)

	go func() {
		for {
			cmd := red.BRPop(context.Background(), 0, *redisQueueName)
			if err := cmd.Err(); err != nil {
				log.Printf("Failed to pop from queue: %v", err)
				continue
			}

			time.Sleep(time.Millisecond * 10)
			consumeCounter.Inc()
		}
	}()
}

func printUsage() {
	println("Usage: app <producer|consumer> [flags]")
	os.Exit(1)
}
